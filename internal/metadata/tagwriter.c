// Rewrites a file's tags by remuxing it (no re-encoding): every stream is copied packet for packet, and the
// container metadata is replaced. Attached pictures (cover art) are carried over where the output container
// supports them.
#include <string.h>
#include <libavformat/avformat.h>
#include <libavutil/dict.h>

#include "tagwriter.h"

static int is_picture(const AVStream *st) { return (st->disposition & AV_DISPOSITION_ATTACHED_PIC) != 0; }

int mp_write_tags(const char *in, const char *out, const char **keys, const char **values, int n, char *err, int errlen)
{
    AVFormatContext *ic = NULL, *oc = NULL;
    AVPacket *pkt = NULL;
    int *map = NULL;
    int r;

    if ((r = avformat_open_input(&ic, in, NULL, NULL)) < 0)
        goto fail;
    if ((r = avformat_find_stream_info(ic, NULL)) < 0)
        goto fail;
    if ((r = avformat_alloc_output_context2(&oc, NULL, NULL, out)) < 0 || !oc) {
        if (r >= 0)
            r = AVERROR_MUXER_NOT_FOUND;
        goto fail;
    }
    // Ogg and WAV cannot hold picture streams: they are dropped there.
    const int keep_pictures = strcmp(oc->oformat->name, "ogg") != 0 && strcmp(oc->oformat->name, "wav") != 0
                              && strcmp(oc->oformat->name, "oga") != 0;

    map = av_calloc(ic->nb_streams, sizeof *map);
    if (!map) {
        r = AVERROR(ENOMEM);
        goto fail;
    }
    for (unsigned i = 0; i < ic->nb_streams; i++) {
        AVStream *ist = ic->streams[i];
        map[i] = -1;
        const int pic = is_picture(ist);
        if (ist->codecpar->codec_type != AVMEDIA_TYPE_AUDIO && !(pic && keep_pictures))
            continue;
        AVStream *ost = avformat_new_stream(oc, NULL);
        if (!ost) {
            r = AVERROR(ENOMEM);
            goto fail;
        }
        if ((r = avcodec_parameters_copy(ost->codecpar, ist->codecpar)) < 0)
            goto fail;
        ost->codecpar->codec_tag = 0;
        ost->time_base = ist->time_base;
        ost->disposition = ist->disposition;
        av_dict_copy(&ost->metadata, ist->metadata, 0);
        map[i] = ost->index;
    }

    // New metadata: the old tags, with each given key set (or removed when its value is empty). Ogg keeps its
    // comments on the stream, the other containers globally; both are written.
    av_dict_copy(&oc->metadata, ic->metadata, 0);
    for (int k = 0; k < n; k++) {
        const char *v = values[k] && values[k][0] ? values[k] : NULL;
        av_dict_set(&oc->metadata, keys[k], v, 0);
        for (unsigned i = 0; i < oc->nb_streams; i++)
            if (oc->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO)
                av_dict_set(&oc->streams[i]->metadata, keys[k], v, 0);
    }

    if (!(oc->oformat->flags & AVFMT_NOFILE) && (r = avio_open(&oc->pb, out, AVIO_FLAG_WRITE)) < 0)
        goto fail;
    if ((r = avformat_write_header(oc, NULL)) < 0)
        goto fail;

    // Pictures are not returned by av_read_frame: write them from the demuxer's copy first.
    for (unsigned i = 0; i < ic->nb_streams; i++) {
        AVStream *ist = ic->streams[i];
        if (map[i] < 0 || !is_picture(ist) || ist->attached_pic.size <= 0)
            continue;
        AVPacket *pic = av_packet_clone(&ist->attached_pic);
        if (!pic) {
            r = AVERROR(ENOMEM);
            goto fail;
        }
        pic->stream_index = map[i];
        pic->pts = pic->dts = 0;
        r = av_interleaved_write_frame(oc, pic);
        av_packet_free(&pic);
        if (r < 0)
            goto fail;
    }

    pkt = av_packet_alloc();
    if (!pkt) {
        r = AVERROR(ENOMEM);
        goto fail;
    }
    while (av_read_frame(ic, pkt) >= 0) {
        const int si = pkt->stream_index;
        if (si < 0 || (unsigned)si >= ic->nb_streams || map[si] < 0 || is_picture(ic->streams[si])) {
            av_packet_unref(pkt);
            continue;
        }
        AVStream *ost = oc->streams[map[si]];
        av_packet_rescale_ts(pkt, ic->streams[si]->time_base, ost->time_base);
        pkt->stream_index = map[si];
        pkt->pos = -1;
        if ((r = av_interleaved_write_frame(oc, pkt)) < 0)
            goto fail;
    }
    if ((r = av_write_trailer(oc)) < 0)
        goto fail;
    r = 0;

fail:
    if (r < 0 && err)
        av_strerror(r, err, (size_t)errlen);
    av_packet_free(&pkt);
    av_free(map);
    if (oc && !(oc->oformat->flags & AVFMT_NOFILE))
        avio_closep(&oc->pb);
    avformat_free_context(oc);
    avformat_close_input(&ic);
    return r;
}
