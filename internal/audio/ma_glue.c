#include "ma_glue.h"

#include <stdlib.h>
#include <string.h>

#if defined(__SSE2__) || defined(_M_X64)
#include <xmmintrin.h>
#define MP_HAVE_FTZ 1
#endif

#include "_cgo_export.h"

static void data_cb(ma_device *dev, void *out, const void *in, ma_uint32 frames)
{
    (void)in;
#ifdef MP_HAVE_FTZ
    // Denormals (decaying filter state) are flushed to zero instead of taking the slow path.
    _mm_setcsr(_mm_getcsr() | 0x8040);
#endif
    goMaData((uintptr_t)dev->pUserData, out, frames);
}

static void notification_cb(const ma_device_notification *n)
{
    goMaNotify((uintptr_t)n->pDevice->pUserData, (int)n->type);
}

ma_context *mp_context_new(int nullBackend)
{
    ma_context *ctx = (ma_context *)calloc(1, sizeof(ma_context));
    if (!ctx)
        return NULL;
    ma_result r;
    if (nullBackend) {
        const ma_backend backends[] = {ma_backend_null};
        r = ma_context_init(backends, 1, NULL, ctx);
    } else {
        r = ma_context_init(NULL, 0, NULL, ctx); // WASAPI first on Windows
    }
    if (r != MA_SUCCESS) {
        free(ctx);
        return NULL;
    }
    return ctx;
}

void mp_context_free(ma_context *ctx)
{
    if (!ctx)
        return;
    ma_context_uninit(ctx);
    free(ctx);
}

int mp_context_is_wasapi(ma_context *ctx) { return ctx->backend == ma_backend_wasapi; }

const char *mp_context_backend_name(ma_context *ctx) { return ma_get_backend_name(ctx->backend); }

int mp_get_devices(ma_context *ctx, ma_device_info **infos, uint32_t *count)
{
    ma_uint32 n = 0;
    if (ma_context_get_devices(ctx, infos, &n, NULL, NULL) != MA_SUCCESS)
        return 0;
    *count = n;
    return 1;
}

ma_device_info *mp_devinfo_at(ma_device_info *infos, uint32_t i) { return &infos[i]; }

ma_result mp_device_open(ma_context *ctx, const ma_device_id *id, ma_format format, int exclusive, uint32_t rate,
                         uintptr_t handle, ma_device **out)
{
    *out = NULL;
    ma_device *dev = (ma_device *)calloc(1, sizeof(ma_device));
    if (!dev)
        return MA_OUT_OF_MEMORY;
    ma_device_config cfg = ma_device_config_init(ma_device_type_playback);
    cfg.playback.pDeviceID = id;
    cfg.playback.format = format;
    cfg.playback.channels = 2;
    cfg.playback.shareMode = exclusive ? ma_share_mode_exclusive : ma_share_mode_shared;
    cfg.sampleRate = rate; // 0 in shared mode: the device's mix rate
    // Small device buffers: the decoder keeps a 750 ms ring buffer filled ahead of the callback, so a short period
    // is safe, seeks are heard sooner, and the clock's latency estimate has less to be wrong about.
    cfg.performanceProfile = ma_performance_profile_low_latency;
    cfg.dataCallback = data_cb;
    cfg.notificationCallback = notification_cb;
    cfg.pUserData = (void *)handle;
    ma_result r = ma_device_init(ctx, &cfg, dev);
    if (r != MA_SUCCESS) {
        free(dev);
        return r;
    }
    *out = dev;
    return MA_SUCCESS;
}

ma_result mp_device_start(ma_device *dev) { return ma_device_start(dev); }

void mp_device_close(ma_device *dev)
{
    if (!dev)
        return;
    ma_device_uninit(dev);
    free(dev);
}

void mp_device_query(ma_device *dev, mp_devinfo *out)
{
    memset(out, 0, sizeof *out);
    out->sampleRate = dev->sampleRate;
    out->internalSampleRate = dev->playback.internalSampleRate;
    out->internalPeriodSize = dev->playback.internalPeriodSizeInFrames;
    out->internalPeriods = dev->playback.internalPeriods;
    out->internalChannels = dev->playback.internalChannels;
    out->internalFormat = (int)dev->playback.internalFormat;
    out->format = (int)dev->playback.format;
    out->shareMode = (int)dev->playback.shareMode;
    strncpy(out->name, dev->playback.name, sizeof out->name - 1);
}
