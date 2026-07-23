#include <crt_externs.h>
#include <errno.h>
#include <libproc.h>
#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
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

static int is_app_macos_executable(void) {
    char path[PROC_PIDPATHINFO_MAXSIZE];
    int length = proc_pidpath(getpid(), path, sizeof(path));

    if (length <= 0) {
        return 0;
    }
    if ((size_t)length >= sizeof(path)) {
        length = (int)sizeof(path) - 1;
    }
    path[length] = '\0';

    return strstr(path, ".app/Contents/MacOS/") != NULL;
}

static int should_append_argv(void) {
    int argc = *_NSGetArgc();
    char **argv = *_NSGetArgv();
    const char *electron_run_as_node = getenv("ELECTRON_RUN_AS_NODE");

    if (!is_app_macos_executable()) {
        return 0;
    }
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

static int has_suffix(const char *value, const char *suffix) {
    size_t value_length = strlen(value);
    size_t suffix_length = strlen(suffix);

    if (suffix_length > value_length) {
        return 0;
    }
    return strcmp(value + value_length - suffix_length, suffix) == 0;
}

static int is_cursor_network_process(void) {
    char path[PROC_PIDPATHINFO_MAXSIZE];
    int length = proc_pidpath(getpid(), path, sizeof(path));
    const char *allowed_suffixes[] = {
        "/MacOS/Cursor",
        "/MacOS/Cursor.real",
        "/MacOS/Cursor Helper",
        "/MacOS/Cursor Helper (GPU)",
        "/MacOS/Cursor Helper (Plugin)",
        "/MacOS/Cursor Helper (Renderer)",
    };

    if (length <= 0) {
        return 0;
    }
    if ((size_t)length >= sizeof(path)) {
        length = (int)sizeof(path) - 1;
    }
    path[length] = '\0';

    for (size_t i = 0; i < sizeof(allowed_suffixes) / sizeof(allowed_suffixes[0]); i++) {
        if (has_suffix(path, allowed_suffixes[i])) {
            return 1;
        }
    }
    return 0;
}

static uint16_t read_redirect_port(void) {
    const char *value = getenv("DVC_CLYDE_REDIRECT_PORT");
    char *end = NULL;
    long port;

    if (value == NULL || value[0] == '\0') {
        return 0;
    }
    errno = 0;
    port = strtol(value, &end, 10);
    if (errno != 0 || end == value || *end != '\0' || port < 1 || port > 65535) {
        return 0;
    }
    return (uint16_t)port;
}

static int redirected_address(const struct sockaddr *address, socklen_t address_length, uint16_t port, struct sockaddr_storage *storage, socklen_t *storage_length) {
    if (address == NULL || storage == NULL || storage_length == NULL) {
        return 0;
    }
    if (address->sa_family == AF_INET) {
        const struct sockaddr_in *source = (const struct sockaddr_in *)address;
        struct sockaddr_in target;

        if (address_length < (socklen_t)sizeof(*source) || ntohs(source->sin_port) != 443) {
            return 0;
        }
        target = *source;
        target.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
        target.sin_port = htons(port);
        memcpy(storage, &target, sizeof(target));
        *storage_length = (socklen_t)sizeof(target);
        return 1;
    }
    if (address->sa_family == AF_INET6) {
        const struct sockaddr_in6 *source = (const struct sockaddr_in6 *)address;
        struct sockaddr_in6 target;

        if (address_length < (socklen_t)sizeof(*source) || ntohs(source->sin6_port) != 443) {
            return 0;
        }
        target = *source;
        target.sin6_addr = in6addr_loopback;
        target.sin6_port = htons(port);
        target.sin6_scope_id = 0;
        memcpy(storage, &target, sizeof(target));
        *storage_length = (socklen_t)sizeof(target);
        return 1;
    }
    return 0;
}

static int clyde_connect(int socket_fd, const struct sockaddr *address, socklen_t address_length) {
    uint16_t port = read_redirect_port();
    struct sockaddr_storage storage;
    socklen_t storage_length = 0;

    if (port == 0 || !is_cursor_network_process() || !redirected_address(address, address_length, port, &storage, &storage_length)) {
        return connect(socket_fd, address, address_length);
    }
    return connect(socket_fd, (const struct sockaddr *)&storage, storage_length);
}

struct interpose_pair {
    const void *replacement;
    const void *replacee;
};

__attribute__((used)) static struct interpose_pair clyde_interposers[] __attribute__((section("__DATA,__interpose"))) = {
    {(const void *)clyde_connect, (const void *)connect},
};

__attribute__((constructor))
static void clyde_inject_main(void) {
    const char *policy_path = getenv("DVC_CLYDE_INJECT_POLICY");

    apply_policy(policy_path);
}
