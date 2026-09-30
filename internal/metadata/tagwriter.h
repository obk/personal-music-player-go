#pragma once

// Remuxes `in` to `out` with the given tags set (an empty value removes the tag). Returns 0 or an AVERROR code,
// with a description in err.
int mp_write_tags(const char *in, const char *out, const char **keys, const char **values, int n, char *err, int errlen);
