#include <crt_externs.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

enum record_kind {
    record_kind_unknown = 0,
    record_kind_set,
    record_kind_unset,
    record_kind_append_argv,
};

struct token_reader {
    char *data;
    size_t length;
    size_t offset;
};

static char *next_token(struct token_reader *reader) {
    if (reader->offset >= reader->length) {
        return NULL;
    }
    char *token = reader->data + reader->offset;
    size_t remaining = reader->length - reader->offset;
    char *end = memchr(token, '\0', remaining);
    if (end == NULL) {
        reader->offset = reader->length;
        return NULL;
    }
    reader->offset = (size_t)(end - reader->data) + 1;
    return token;
}

static enum record_kind parse_record_kind(const char *value) {
    if (strcmp(value, "set") == 0) {
        return record_kind_set;
    }
    if (strcmp(value, "unset") == 0) {
        return record_kind_unset;
    }
    if (strcmp(value, "append-argv") == 0) {
        return record_kind_append_argv;
    }
    return record_kind_unknown;
}

static int read_policy_file(const char *path, char **data_out, size_t *length_out) {
    FILE *file = fopen(path, "rb");
    long length;
    char *data;

    if (file == NULL) {
        return -1;
    }
    if (fseek(file, 0, SEEK_END) != 0) {
        fclose(file);
        return -1;
    }
    length = ftell(file);
    if (length < 0) {
        fclose(file);
        return -1;
    }
    if (fseek(file, 0, SEEK_SET) != 0) {
        fclose(file);
        return -1;
    }
    data = calloc((size_t)length + 1, 1);
    if (data == NULL) {
        fclose(file);
        return -1;
    }
    if (length > 0 && fread(data, 1, (size_t)length, file) != (size_t)length) {
        free(data);
        fclose(file);
        return -1;
    }
    fclose(file);
    *data_out = data;
    *length_out = (size_t)length;
    return 0;
}

static int should_append_argv(void) {
    int argc = *_NSGetArgc();
    char **argv = *_NSGetArgv();
    const char *electron_run_as_node = getenv("ELECTRON_RUN_AS_NODE");

    if (electron_run_as_node != NULL && strcmp(electron_run_as_node, "1") == 0) {
        return 0;
    }
    for (int i = 0; i < argc; i++) {
        if (argv[i] != NULL && strncmp(argv[i], "--type=", 7) == 0) {
            return 0;
        }
    }
    return 1;
}

static void append_argv_value(const char *value) {
    int *argc_ptr = _NSGetArgc();
    char ***argv_ptr = _NSGetArgv();
    int argc = *argc_ptr;
    char **argv = *argv_ptr;
    char **next_argv = calloc((size_t)argc + 2, sizeof(char *));

    if (next_argv == NULL) {
        return;
    }
    for (int i = 0; i < argc; i++) {
        next_argv[i] = argv[i];
    }
    next_argv[argc] = strdup(value);
    next_argv[argc + 1] = NULL;
    if (next_argv[argc] == NULL) {
        free(next_argv);
        return;
    }
    *argv_ptr = next_argv;
    *argc_ptr = argc + 1;
}

static void apply_policy(const char *path) {
    char *data = NULL;
    size_t length = 0;
    int can_append = should_append_argv();

    if (path == NULL || path[0] == '\0') {
        return;
    }
    if (read_policy_file(path, &data, &length) != 0) {
        return;
    }

    struct token_reader reader = {
        .data = data,
        .length = length,
        .offset = 0,
    };

    for (;;) {
        char *kind_token = next_token(&reader);
        enum record_kind kind;

        if (kind_token == NULL) {
            break;
        }
        kind = parse_record_kind(kind_token);
        switch (kind) {
        case record_kind_set: {
            char *key = next_token(&reader);
            char *value = next_token(&reader);
            if (key == NULL || value == NULL) {
                free(data);
                return;
            }
            setenv(key, value, 1);
            break;
        }
        case record_kind_unset: {
            char *key = next_token(&reader);
            if (key == NULL) {
                free(data);
                return;
            }
            unsetenv(key);
            break;
        }
        case record_kind_append_argv: {
            char *value = next_token(&reader);
            if (value == NULL) {
                free(data);
                return;
            }
            if (can_append) {
                append_argv_value(value);
            }
            break;
        }
        case record_kind_unknown:
            free(data);
            return;
        }
    }
    free(data);
}

__attribute__((constructor))
static void clyde_inject_main(void) {
    const char *policy_path = getenv("DVC_CLYDE_INJECT_POLICY");

    unsetenv("DYLD_INSERT_LIBRARIES");
    apply_policy(policy_path);
}
