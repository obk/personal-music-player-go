# Local changes to miniaudio.h

`miniaudio.h` is vendored from upstream with one change. Re-apply it when updating miniaudio.

## WASAPI exclusive mode: honour the requested format

**Where:** `ma_device_init_internal__wasapi`, the `pData->shareMode == ma_share_mode_exclusive` branch (search for
`personal-music-player patch`).

**Why:** upstream always opens an exclusive-mode stream in the device's configured "Default Format" (Windows Sound
settings → device → Advanced) and lets miniaudio's converter resample and convert the client's audio to it. A
bit-perfect player needs the device opened at the source's own sample rate and sample format instead.

**What:** before reading the Default Format, the patch builds a `WAVEFORMATEXTENSIBLE` from the requested format,
channel count and sample rate and asks `IAudioClient::IsFormatSupported` (exclusive) whether the device accepts it.
A 32-bit integer request is also tried as 24 valid bits in a 32-bit container. The first accepted candidate is
used; if none is accepted, the upstream behaviour (Default Format) applies unchanged.
