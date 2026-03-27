/* C API exported by macos/GoBridge (buildmode=c-archive). Keep in sync with fp_export.go */
#ifndef NEOFS_FP_API_H
#define NEOFS_FP_API_H

#ifdef __cplusplus
extern "C" {
#endif

int NeoFsFpVersion(void);
/* Returns 0 on success; negative error code on failure. */
int NeoFsFpInit(const char *endpoint, const char *wallet_key_path);
void NeoFsFpShutdown(void);

#ifdef __cplusplus
}
#endif

#endif
