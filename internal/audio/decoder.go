package audio

/*
#cgo pkg-config: libavformat libavcodec libavutil libswresample
#include <stdlib.h>
#include <string.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/channel_layout.h>
#include <libavutil/error.h>
#include <libavutil/log.h>
#include <libavutil/mathematics.h>
#include <libavutil/samplefmt.h>
#include <libswresample/swresample.h>

static int mp_eagain(void) { return AVERROR(EAGAIN); }
static int mp_eof(void) { return AVERROR_EOF; }
static int64_t mp_nopts(void) { return AV_NOPTS_VALUE; }
static int64_t mp_time_base(void) { return AV_TIME_BASE; }
static AVStream *mp_stream(AVFormatContext *f, int i) { return f->streams[i]; }
static const char *mp_format_name(AVFormatContext *f) { return f->iformat && f->iformat->name ? f->iformat->name : ""; }
static int64_t mp_rescale_q(int64_t a, int bn, int bd, int cn, int cd) {
	return av_rescale_q(a, (AVRational){bn, bd}, (AVRational){cn, cd});
}
*/
import "C"

import (
	"errors"
	"sort"
	"unsafe"
)

// SampleFormat is the decoder's output format. S24 is packed (3 bytes per sample) and S32 carries 24-bit sources
// as value << 8, both without conversion.
type SampleFormat int

const (
	F32 SampleFormat = iota
	S16
	S24
	S32
)

func (f SampleFormat) BytesPerSample() int {
	switch f {
	case S16:
		return 2
	case S24:
		return 3
	}
	return 4
}

// SourceInfo describes the file's audio as decoded.
type SourceInfo struct {
	Rate     int
	Channels int
	Bits     int  // bits per sample actually carried by the source (24 for 24-bit FLAC in s32)
	IsFloat  bool // decoder produces floating point (lossy codecs, float WAV)
}

var (
	errEAGAIN = C.mp_eagain()
	errEOF    = C.mp_eof()
	noPTS     = int64(C.mp_nopts())
)

func init() { C.av_log_set_level(C.AV_LOG_ERROR) }

func avError(code C.int) string {
	var buf [C.AV_ERROR_MAX_STRING_SIZE]C.char
	C.av_strerror(code, &buf[0], C.size_t(len(buf)))
	return C.GoString(&buf[0])
}

type indexEntry struct {
	pos         int64
	startSample int64 // raw sample index of the packet's first sample
	samples     int64 // samples the packet decodes to
}

// Decoder decodes any file FFmpeg can read to interleaved stereo at a fixed output rate and sample format
// (libavformat -> libavcodec -> libswresample). One instance per open file; not safe for concurrent use: use it
// from a single (decoder) goroutine.
type Decoder struct {
	fmt         *C.AVFormatContext
	ctx         *C.AVCodecContext
	st          *C.AVStream
	pkt         *C.AVPacket
	frame       *C.AVFrame
	swr         *C.SwrContext
	streamIndex C.int

	outRate   int
	outFormat SampleFormat
	bpf       int // bytes per stereo frame in buf (packed S24 is staged as S32)
	outBpf    int // bytes per stereo frame handed out by Read
	src       SourceInfo
	duration  int64
	pos       int64

	// Signature of the frames the current resampler was built for.
	swrRate, swrFormat, swrChannels C.int

	// Converted output waiting to be handed out: frames [rd, wr) of buf (bpf bytes per frame). C memory, since
	// swresample writes into it through a pointer the decoder keeps.
	buf       unsafe.Pointer
	bufFrames int
	rd, wr    int

	eofRead        bool  // demuxer exhausted, drain packet sent
	swrFlushed     bool  // resampler tail delivered; nothing more will come
	discarding     bool  // after a seek: skipping samples before the target
	discardTarget  int64 // in output frames
	inCursor       int64 // running input-sample position when frames carry no pts
	firstAfterSeek bool
	firstRawStart  int64
	firstSamples   int64
	byteSeek       bool
	indexBuilt     bool
	index          []indexEntry
	delaySamples   int64 // encoder delay trimmed by the decoder (start_time), in stream samples
	cursorTiming   bool  // discard logic uses inCursor instead of frame timestamps
	ptrs           [64]*C.uint8_t
}

// Formats whose demuxer timestamps are only estimates after a seek (MP3, raw ADTS AAC) are seeked by byte offset
// using a packet index built once from a sequential scan, so positions stay sample-exact. The decoder re-applies
// its start trim to the first frame after every flush, so that frame is shorter than its packet; the difference
// tells us where its content really starts.

// OpenDecoder opens path. outRate 0 = the source's own sample rate.
func OpenDecoder(path string, outRate int, format SampleFormat) (*Decoder, error) {
	d := &Decoder{inCursor: -1, swrFormat: -1}
	d.setFormat(format)
	fail := func(msg string) (*Decoder, error) {
		d.Close()
		return nil, errors.New(msg)
	}

	cpath := C.CString(path) // FFmpeg's file protocol takes UTF-8 on Windows
	defer C.free(unsafe.Pointer(cpath))
	if r := C.avformat_open_input(&d.fmt, cpath, nil, nil); r < 0 {
		return fail("Cannot open file: " + avError(r))
	}
	if r := C.avformat_find_stream_info(d.fmt, nil); r < 0 {
		return fail("Cannot read stream info: " + avError(r))
	}
	var codec *C.AVCodec
	d.streamIndex = C.av_find_best_stream(d.fmt, C.AVMEDIA_TYPE_AUDIO, -1, -1, &codec, 0)
	if d.streamIndex < 0 || codec == nil {
		return fail("No decodable audio stream")
	}
	d.st = C.mp_stream(d.fmt, d.streamIndex)

	d.ctx = C.avcodec_alloc_context3(codec)
	if d.ctx == nil || C.avcodec_parameters_to_context(d.ctx, d.st.codecpar) < 0 {
		return fail("Cannot set up decoder")
	}
	if r := C.avcodec_open2(d.ctx, codec, nil); r < 0 {
		return fail("Cannot open decoder: " + avError(r))
	}
	d.pkt = C.av_packet_alloc()
	d.frame = C.av_frame_alloc()
	if d.pkt == nil || d.frame == nil {
		return fail("Out of memory")
	}

	sfmt := d.ctx.sample_fmt
	d.src.Rate = int(d.ctx.sample_rate)
	if d.src.Rate <= 0 {
		d.src.Rate = int(d.st.codecpar.sample_rate)
	}
	d.src.Channels = int(d.ctx.ch_layout.nb_channels)
	d.src.IsFloat = sfmt == C.AV_SAMPLE_FMT_FLT || sfmt == C.AV_SAMPLE_FMT_FLTP || sfmt == C.AV_SAMPLE_FMT_DBL ||
		sfmt == C.AV_SAMPLE_FMT_DBLP
	if d.ctx.bits_per_raw_sample > 0 {
		d.src.Bits = int(d.ctx.bits_per_raw_sample)
	} else {
		d.src.Bits = int(C.av_get_bytes_per_sample(sfmt)) * 8
	}
	if d.src.Rate <= 0 {
		return fail("Unknown sample rate")
	}
	d.outRate = outRate
	if d.outRate <= 0 {
		d.outRate = d.src.Rate
	}

	seconds := 0.0
	if int64(d.fmt.duration) != noPTS {
		seconds = float64(d.fmt.duration) / float64(C.mp_time_base())
	} else if int64(d.st.duration) != noPTS {
		seconds = float64(d.st.duration) * float64(d.st.time_base.num) / float64(d.st.time_base.den)
	}
	d.duration = int64(seconds*float64(d.outRate) + 0.5)
	name := C.GoString(C.mp_format_name(d.fmt))
	d.byteSeek = name == "mp3" || name == "aac"
	return d, nil
}

// Close frees everything. Safe on a partly opened decoder.
func (d *Decoder) Close() {
	C.swr_free(&d.swr)
	C.av_frame_free(&d.frame)
	C.av_packet_free(&d.pkt)
	C.avcodec_free_context(&d.ctx)
	C.avformat_close_input(&d.fmt)
	if d.buf != nil {
		C.free(d.buf)
		d.buf = nil
	}
}

func (d *Decoder) setFormat(f SampleFormat) {
	d.outFormat = f
	d.outBpf = 2 * f.BytesPerSample()
	d.bpf = d.outBpf
	if f == S24 {
		d.bpf = 8
	}
}

func (d *Decoder) avOutFormat() C.enum_AVSampleFormat {
	switch d.outFormat {
	case S16:
		return C.AV_SAMPLE_FMT_S16
	case S24, S32: // FFmpeg has no packed 24-bit format: staged as S32, packed in Read
		return C.AV_SAMPLE_FMT_S32
	}
	return C.AV_SAMPLE_FMT_FLT
}

// SetOutput changes the output rate (0 = source rate) and sample format. Only valid before the first Read or SeekTo.
func (d *Decoder) SetOutput(outRate int, format SampleFormat) {
	rate := outRate
	if rate <= 0 {
		rate = d.src.Rate
	}
	if d.outRate > 0 && rate != d.outRate {
		d.duration = int64(C.av_rescale(C.int64_t(d.duration), C.int64_t(rate), C.int64_t(d.outRate)))
	}
	d.outRate = rate
	d.setFormat(format)
	C.swr_free(&d.swr) // rebuilt for the new output on the first frame
	d.rd, d.wr = 0, 0
}

func (d *Decoder) OutRate() int            { return d.outRate }
func (d *Decoder) OutFormat() SampleFormat { return d.outFormat }
func (d *Decoder) BytesPerFrame() int      { return d.outBpf }
func (d *Decoder) Source() SourceInfo      { return d.src }
func (d *Decoder) DurationFrames() int64   { return d.duration } // 0 if unknown
func (d *Decoder) PositionFrames() int64   { return d.pos }      // index of the next frame Read returns

// reserve makes room for frames more frames at wr and returns a pointer to it.
func (d *Decoder) reserve(frames int) unsafe.Pointer {
	if d.rd == d.wr {
		d.rd, d.wr = 0, 0
	}
	if d.wr+frames > d.bufFrames {
		n := d.wr + frames
		d.buf = C.realloc(d.buf, C.size_t(n*d.bpf))
		d.bufFrames = n
	}
	return unsafe.Add(d.buf, d.wr*d.bpf)
}

func (d *Decoder) ensureSwr(f *C.AVFrame) bool {
	chans := f.ch_layout.nb_channels
	if d.swr != nil && d.swrRate == f.sample_rate && d.swrFormat == f.format && d.swrChannels == chans {
		return true
	}
	C.swr_free(&d.swr)

	var in, out C.AVChannelLayout
	if f.ch_layout.order == C.AV_CHANNEL_ORDER_UNSPEC {
		C.av_channel_layout_default(&in, chans)
	} else {
		C.av_channel_layout_copy(&in, &f.ch_layout)
	}
	C.av_channel_layout_default(&out, 2)
	r := C.swr_alloc_set_opts2(&d.swr, &out, d.avOutFormat(), C.int(d.outRate), &in,
		C.enum_AVSampleFormat(f.format), f.sample_rate, 0, nil)
	if r >= 0 {
		r = C.swr_init(d.swr)
	}
	C.av_channel_layout_uninit(&in)
	C.av_channel_layout_uninit(&out)
	if r < 0 {
		C.swr_free(&d.swr)
		return false
	}
	d.swrRate, d.swrFormat, d.swrChannels = f.sample_rate, f.format, chans
	return true
}

// convert turns one decoded frame into buf, honouring a pending post-seek discard.
func (d *Decoder) convert(f *C.AVFrame) bool {
	nb := int64(f.nb_samples)
	var skipIn int64
	rate := int64(f.sample_rate)

	if d.discarding {
		targetIn := int64(C.av_rescale(C.int64_t(d.discardTarget), C.int64_t(rate), C.int64_t(d.outRate)))
		var startIn int64
		switch {
		case d.cursorTiming:
			if d.firstAfterSeek {
				startIn = d.firstRawStart + (d.firstSamples - nb) - d.delaySamples
				d.firstAfterSeek = false
			} else {
				startIn = d.inCursor
			}
		case int64(f.best_effort_timestamp) != noPTS:
			base := int64(0)
			if int64(d.st.start_time) != noPTS {
				base = int64(d.st.start_time)
			}
			startIn = int64(C.mp_rescale_q(C.int64_t(int64(f.best_effort_timestamp)-base), d.st.time_base.num,
				d.st.time_base.den, 1, f.sample_rate))
		case d.inCursor >= 0:
			startIn = d.inCursor
		default:
			startIn = targetIn
		}
		d.inCursor = startIn + nb
		if startIn+nb <= targetIn {
			return true // entirely before the target: drop
		}
		if startIn > targetIn { // the demuxer landed late; report where we really are
			d.pos = int64(C.av_rescale(C.int64_t(startIn), C.int64_t(d.outRate), C.int64_t(rate)))
		}
		skipIn = max(0, targetIn-startIn)
		d.discarding = false
	}

	if !d.ensureSwr(f) {
		return false
	}
	chans := int(f.ch_layout.nb_channels)
	if chans > len(d.ptrs) {
		return false
	}
	sfmt := C.enum_AVSampleFormat(f.format)
	bps := int64(C.av_get_bytes_per_sample(sfmt))
	ext := unsafe.Slice(f.extended_data, chans)
	if C.av_sample_fmt_is_planar(sfmt) != 0 {
		for c := 0; c < chans; c++ {
			d.ptrs[c] = (*C.uint8_t)(unsafe.Add(unsafe.Pointer(ext[c]), skipIn*bps))
		}
	} else {
		d.ptrs[0] = (*C.uint8_t)(unsafe.Add(unsafe.Pointer(ext[0]), skipIn*bps*int64(chans)))
	}

	inCount := C.int(nb - skipIn)
	capacity := C.swr_get_out_samples(d.swr, inCount)
	if capacity <= 0 {
		return true
	}
	out := (*C.uint8_t)(d.reserve(int(capacity)))
	got := C.swr_convert(d.swr, &out, capacity, &d.ptrs[0], inCount)
	if got < 0 {
		return false
	}
	d.wr += int(got)
	return true
}

func (d *Decoder) flushSwr() {
	if d.swr == nil {
		return
	}
	for {
		capacity := max(4096, int(C.swr_get_out_samples(d.swr, 0)))
		out := (*C.uint8_t)(d.reserve(capacity))
		got := C.swr_convert(d.swr, &out, C.int(capacity), nil, 0)
		if got <= 0 {
			break
		}
		d.wr += int(got)
	}
}

func (d *Decoder) buildIndex() bool {
	if d.indexBuilt {
		return len(d.index) > 0
	}
	d.indexBuilt = true
	rate := d.st.codecpar.sample_rate
	if rate <= 0 || C.av_seek_frame(d.fmt, d.streamIndex, 0, C.AVSEEK_FLAG_BYTE) < 0 {
		return false
	}
	tb := d.st.time_base
	var cumTb int64
	for C.av_read_frame(d.fmt, d.pkt) >= 0 {
		if d.pkt.stream_index == d.streamIndex && d.pkt.pos >= 0 && d.pkt.duration > 0 {
			d.index = append(d.index, indexEntry{
				pos:         int64(d.pkt.pos),
				startSample: int64(C.mp_rescale_q(C.int64_t(cumTb), tb.num, tb.den, 1, rate)),
				samples:     int64(C.mp_rescale_q(d.pkt.duration, tb.num, tb.den, 1, rate)),
			})
			cumTb += int64(d.pkt.duration)
		}
		C.av_packet_unref(d.pkt)
	}
	if int64(d.st.start_time) != noPTS {
		d.delaySamples = int64(C.mp_rescale_q(d.st.start_time, tb.num, tb.den, 1, rate))
	}
	return len(d.index) > 0
}

// fill makes sure at least one converted frame is available. False at end of stream or on a fatal error.
func (d *Decoder) fill() bool {
	for d.rd == d.wr {
		if d.swrFlushed {
			return false
		}
		r := C.avcodec_receive_frame(d.ctx, d.frame)
		if r == 0 {
			ok := d.convert(d.frame)
			C.av_frame_unref(d.frame)
			if !ok {
				return false
			}
			continue
		}
		if r == errEOF || (r == errEAGAIN && d.eofRead) {
			d.flushSwr()
			d.swrFlushed = true
			continue
		}
		if r != errEAGAIN {
			return false
		}
		// The decoder wants more input.
		if C.av_read_frame(d.fmt, d.pkt) < 0 {
			C.avcodec_send_packet(d.ctx, nil) // enter draining mode
			d.eofRead = true
			continue
		}
		if d.pkt.stream_index == d.streamIndex {
			C.avcodec_send_packet(d.ctx, d.pkt) // corrupt packets are skipped, not fatal
		}
		C.av_packet_unref(d.pkt)
	}
	return true
}

// SeekTo positions the decoder so the next Read returns output frame `frame`. The demuxer seeks to the preceding
// keyframe, then samples before `frame` are decoded and discarded, so the result is exact (when no sample-rate
// conversion is involved).
func (d *Decoder) SeekTo(frame int64) bool {
	frame = max(0, frame)
	if d.duration > 0 {
		frame = min(frame, d.duration)
	}
	var r C.int = -1
	d.cursorTiming = false
	d.inCursor = -1
	if d.byteSeek && frame > 0 && d.buildIndex() {
		// Start a few packets early: the MP3 bit reservoir and AAC overlap need history to decode cleanly.
		inRate := int64(d.st.codecpar.sample_rate)
		targetIn := int64(C.av_rescale(C.int64_t(frame), C.int64_t(inRate), C.int64_t(d.outRate))) + d.delaySamples
		j := sort.Search(len(d.index), func(i int) bool { return targetIn < d.index[i].startSample }) - 1
		j = max(j, 0)
		start := max(j-4, 0)
		r = C.av_seek_frame(d.fmt, d.streamIndex, C.int64_t(d.index[start].pos), C.AVSEEK_FLAG_BYTE)
		d.firstAfterSeek = true
		d.firstRawStart = d.index[start].startSample
		d.firstSamples = d.index[start].samples
		d.cursorTiming = true
	} else {
		tb := d.st.time_base
		ts := C.mp_rescale_q(C.int64_t(frame), 1, C.int(d.outRate), tb.num, tb.den)
		if int64(d.st.start_time) != noPTS {
			ts += d.st.start_time
		}
		r = C.av_seek_frame(d.fmt, d.streamIndex, ts, C.AVSEEK_FLAG_BACKWARD)
		if r < 0 {
			r = C.avformat_seek_file(d.fmt, d.streamIndex, C.INT64_MIN, ts, ts, 0)
		}
	}
	if r < 0 && frame != 0 {
		return false
	}

	C.avcodec_flush_buffers(d.ctx)
	C.swr_free(&d.swr) // rebuilt on the next frame: no stale filter state across the seek
	d.rd, d.wr = 0, 0
	d.eofRead = false
	d.swrFlushed = false
	// Position 0 is "the first decoded sample" by definition. Some codecs (MP3 with a LAME delay) trim the first
	// frame without adjusting its timestamp, so pts arithmetic must not be used for it.
	d.discarding = frame > 0
	d.discardTarget = frame
	d.pos = frame
	return true
}

// Read fills out with up to len(out)/BytesPerFrame() stereo frames in OutFormat(). It returns the number of
// frames written, 0 at end of stream or on a fatal error.
func (d *Decoder) Read(out []byte) int {
	maxFrames := len(out) / d.outBpf
	done := 0
	for done < maxFrames {
		if d.rd == d.wr && !d.fill() {
			break
		}
		n := min(d.wr-d.rd, maxFrames-done)
		o := out[done*d.outBpf : (done+n)*d.outBpf]
		in := unsafe.Slice((*byte)(unsafe.Add(d.buf, d.rd*d.bpf)), n*d.bpf)
		if d.outFormat == S24 {
			// Little-endian S32 holding a 24-bit sample in its top 3 bytes: keep those bytes (exact for <= 24 bits).
			for i := 0; i < n*2; i++ {
				o[3*i] = in[4*i+1]
				o[3*i+1] = in[4*i+2]
				o[3*i+2] = in[4*i+3]
			}
		} else {
			copy(o, in)
		}
		d.rd += n
		done += n
	}
	d.pos += int64(done)
	return done
}
