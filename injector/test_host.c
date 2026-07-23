#include <crt_externs.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int main(void) {
    const char *inserted = getenv("DYLD_INSERT_LIBRARIES");
    const char *set_value = getenv("DVC_INJECT_TEST");
    const char *removed_value = getenv("DVC_INJECT_REMOVE");
    const char *expect_argv = getenv("DVC_INJECT_EXPECT_ARGV");
    int argc = *_NSGetArgc();
    char **argv = *_NSGetArgv();
    int found_arg = 0;
    int want_argv = 1;

    if (expect_argv != NULL && strcmp(expect_argv, "0") == 0) {
        want_argv = 0;
    }

    if (inserted == NULL) {
        fprintf(stderr, "DYLD_INSERT_LIBRARIES missing\n");
        return 10;
    }
    if (set_value == NULL || strcmp(set_value, "ok") != 0) {
        fprintf(stderr, "DVC_INJECT_TEST missing\n");
        return 11;
    }
    if (removed_value != NULL) {
        fprintf(stderr, "DVC_INJECT_REMOVE still set\n");
        return 12;
    }
    for (int i = 0; i < argc; i++) {
        if (strcmp(argv[i], "--dvc-inject-arg") == 0) {
            found_arg = 1;
        }
    }
    if (want_argv && !found_arg) {
        fprintf(stderr, "argv append missing\n");
        return 13;
    }
    if (!want_argv && found_arg) {
        fprintf(stderr, "argv append should be absent\n");
        return 14;
    }
    return 0;
}
