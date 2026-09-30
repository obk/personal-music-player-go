// Thin C layer between Go and miniaudio: owns the context / device structs (which must not live in Go memory)
// and trampolines the device callbacks into Go.
#pragma once

#include <stdint.h>
#include "miniaudio.h"

typedef struct {
    uint32_t sampleRate;         // pipeline rate (what the callback sees)
    uint32_t internalSampleRate; // what the driver runs at
    uint32_t internalPeriodSize;
    uint32_t internalPeriods;
    uint32_t internalChannels;
    int internalFormat;
    int format;
    int shareMode;
    char name[256];
} mp_devinfo;

ma_context *mp_context_new(int nullBackend);
void mp_context_free(ma_context *ctx);
int mp_context_is_wasapi(ma_context *ctx);
const char *mp_context_backend_name(ma_context *ctx);

// Device list: valid until the next call on the same context.
int mp_get_devices(ma_context *ctx, ma_device_info **infos, uint32_t *count);
ma_device_info *mp_devinfo_at(ma_device_info *infos, uint32_t i);

// Opens (does not start) a playback device. id NULL = default device. handle is passed back to the Go callbacks.
ma_result mp_device_open(ma_context *ctx, const ma_device_id *id, ma_format format, int exclusive, uint32_t rate,
                         uintptr_t handle, ma_device **out);
ma_result mp_device_start(ma_device *dev);
void mp_device_close(ma_device *dev);
void mp_device_query(ma_device *dev, mp_devinfo *out);
