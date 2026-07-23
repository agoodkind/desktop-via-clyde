#include <crt_externs.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int main(void) {
    const char *inserted = getenv("DYLD_INSERT_LIBRARIES");
    const char *mode = getenv("DVC_INJECT_SMOKE_MODE");
    int argc = *_NSGetArgc();
    char **argv = *_NSGetArgv();
    int found_arg = 0;

    if (inserted == NULL) {
        fprintf(stderr, "DYLD_INSERT_LIBRARIES missing\n");
        return 42;
    }
    if (mode == NULL || strcmp(mode, "sentinel") != 0) {
        return 0;
    }
    const char *set_value = getenv("DVC_INJECT_SMOKE_SET");
    if (set_value == NULL || strcmp(set_value, "ok") != 0) {
        fprintf(stderr, "sentinel set action missing\n");
        return 43;
    }
    if (getenv("DVC_INJECT_SMOKE_REMOVE") != NULL) {
        fprintf(stderr, "sentinel unset action missing\n");
        return 44;
    }
    for (int i = 0; i < argc; i++) {
        if (strcmp(argv[i], "--dvc-inject-smoke-arg") == 0) {
            found_arg = 1;
        }
    }
    if (!found_arg) {
        fprintf(stderr, "sentinel argv append missing\n");
        return 45;
    }
    return 0;
}
