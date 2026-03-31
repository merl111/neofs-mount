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

/* Reads config from the given path and connects if needed.
   Returns 0 when client is ready; negative on error. */
int NeoFsFpEnsureClient(const char *config_path);

/* Returns a malloc'd JSON string: [{id, name}, ...]. Caller must free(). NULL on error. */
char *NeoFsFpListContainers(void);

/* Returns a malloc'd JSON string: [{name, objectID, size, isDirectory}, ...].
   prefix may be NULL or "" for top-level listing. Caller must free(). NULL on error. */
char *NeoFsFpListEntries(const char *container_id, const char *prefix);

/* Set the directory for temporary files (must be sandbox-accessible). */
void NeoFsFpSetTempDir(const char *dir);

/* Downloads an object to a temp file. Returns malloc'd path string. Caller must free().
   NULL on error. */
char *NeoFsFpFetchObject(const char *container_id, const char *object_id);

/* Deletes an object. Returns 0 on success; negative on error. */
int NeoFsFpDeleteObject(const char *container_id, const char *object_id);

#ifdef __cplusplus
}
#endif

#endif
