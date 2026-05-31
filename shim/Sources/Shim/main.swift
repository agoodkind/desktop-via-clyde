import Darwin
import Foundation

// Self-locating desktop-via-clyde MITM shim. Replaces
// <App>.app/Contents/MacOS/<ExecName> for any registered target. Resolves its
// own path with _NSGetExecutablePath, derives the sibling .real binary,
// preflights the clyde MITM proxy, applies the target-specific environment
// policy, and execv's the real binary with launch-mode-appropriate proxy
// settings.

enum TargetPolicy: String {
    case codex
    case `default`
}

struct EnvAction {
    let key: String
    let value: String?
}

struct ShimConfig {
    var proxyHost: String
    var proxyPort: UInt16
    var caCertificate: String
    var noProxy: String
    var launchWorkingDirectory: String
    var targetPolicies: [String: TargetPolicy]
}

func homeDir() -> String {
    if let h = ProcessInfo.processInfo.environment["HOME"], !h.isEmpty {
        return h
    }
    return NSHomeDirectory()
}

func defaultShimConfig() -> ShimConfig {
    let stateBase = defaultStateRoot()
    return ShimConfig(
        proxyHost: "::1",
        proxyPort: 48723,
        caCertificate: "\(stateBase)/mitm/ca/clyde-mitm-ca.crt",
        noProxy: "localhost,127.0.0.1,::1,[::1]",
        launchWorkingDirectory: homeDir(),
        targetPolicies: [
            "cursor": .default,
            "codex": .codex,
            "claude": .default,
        ]
    )
}

func configPath() -> String {
    if let xdg = ProcessInfo.processInfo.environment["XDG_CONFIG_HOME"], !xdg.isEmpty {
        return "\(xdg)/desktop-via-clyde/config.toml"
    }
    return "\(homeDir())/.config/desktop-via-clyde/config.toml"
}

func defaultStateRoot() -> String {
    if let xdg = ProcessInfo.processInfo.environment["XDG_STATE_HOME"], !xdg.isEmpty {
        return "\(xdg)/clyde"
    }
    return "\(homeDir())/.local/state/clyde"
}

func loadShimConfig() throws -> ShimConfig {
    let path = configPath()
    guard FileManager.default.fileExists(atPath: path) else {
        throw NSError(domain: "desktop-via-clyde", code: 1, userInfo: [
            NSLocalizedDescriptionKey: "Config file missing at \(path)",
        ])
    }
    let contents = try String(contentsOfFile: path, encoding: .utf8)
    var config = defaultShimConfig()
    var section: [String] = []

    for rawLine in contents.components(separatedBy: .newlines) {
        let line = stripComments(rawLine).trimmingCharacters(in: .whitespacesAndNewlines)
        if line.isEmpty {
            continue
        }
        if line.hasPrefix("[") && line.hasSuffix("]") {
            let name = String(line.dropFirst().dropLast())
            section = name.split(separator: ".").map(String.init)
            continue
        }
        guard let separator = line.firstIndex(of: "=") else {
            continue
        }
        let key = line[..<separator].trimmingCharacters(in: .whitespacesAndNewlines)
        let value = line[line.index(after: separator)...].trimmingCharacters(in: .whitespacesAndNewlines)

        if section == ["proxy"] {
            switch key {
            case "host":
                config.proxyHost = try parseStringValue(value)
            case "port":
                config.proxyPort = try parsePortValue(value)
            case "ca_certificate":
                config.caCertificate = try parseStringValue(value)
            case "no_proxy":
                config.noProxy = try parseStringValue(value)
            case "launch_working_directory":
                config.launchWorkingDirectory = try parseStringValue(value)
            default:
                break
            }
            continue
        }

        if section.count == 2, section[0] == "apps", key == "target_policy" {
            let appID = section[1].lowercased()
            config.targetPolicies[appID] = parseTargetPolicy(try parseStringValue(value))
        }
    }

    return config
}

func stripComments(_ line: String) -> String {
    var inQuotes = false
    var escaped = false
    var output = ""
    for character in line {
        if character == "\\" && inQuotes {
            escaped.toggle()
            output.append(character)
            continue
        }
        if character == "\"" && !escaped {
            inQuotes.toggle()
            output.append(character)
            continue
        }
        escaped = false
        if character == "#" && !inQuotes {
            break
        }
        output.append(character)
    }
    return output
}

func parseStringValue(_ value: String) throws -> String {
    let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
    guard trimmed.count >= 2, trimmed.first == "\"", trimmed.last == "\"" else {
        throw NSError(domain: "desktop-via-clyde", code: 2, userInfo: [
            NSLocalizedDescriptionKey: "Expected quoted TOML string, got \(trimmed)",
        ])
    }
    let inner = trimmed.dropFirst().dropLast()
    return String(inner)
        .replacingOccurrences(of: "\\\"", with: "\"")
        .replacingOccurrences(of: "\\\\", with: "\\")
}

func parsePortValue(_ value: String) throws -> UInt16 {
    let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
    guard let parsed = UInt16(trimmed) else {
        throw NSError(domain: "desktop-via-clyde", code: 3, userInfo: [
            NSLocalizedDescriptionKey: "Expected TOML port number, got \(trimmed)",
        ])
    }
    return parsed
}

func parseTargetPolicy(_ value: String) -> TargetPolicy {
    if value.lowercased() == "codex" {
        return .codex
    }
    return .default
}

func proxyURL(_ config: ShimConfig) -> String {
    return "http://[\(config.proxyHost)]:\(config.proxyPort)"
}

// resolveSelfPath returns the absolute, symlink-resolved path of the running
// shim binary. _NSGetExecutablePath may return a relative or symlinked path.
func resolveSelfPath() -> String? {
    var size: UInt32 = 0
    _ = _NSGetExecutablePath(nil, &size)
    var buf = [CChar](repeating: 0, count: Int(size) + 1)
    let rc = _NSGetExecutablePath(&buf, &size)
    if rc != 0 {
        return nil
    }
    let raw = String(cString: buf)
    var resolved = [CChar](repeating: 0, count: Int(PATH_MAX))
    if realpath(raw, &resolved) == nil {
        return raw
    }
    return String(cString: resolved)
}

func showAlertAndExit(_ message: String, _ exitCode: Int32) -> Never {
    let escaped = message.replacingOccurrences(of: "\"", with: "\\\"")
    let script = "display alert \"desktop-via-clyde shim\" message \"\(escaped)\" as critical"
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
    process.arguments = ["-e", script]
    process.standardOutput = FileHandle.nullDevice
    process.standardError = FileHandle.nullDevice
    do {
        try process.run()
        process.waitUntilExit()
    } catch {
        // best-effort dialog; fall through to stderr write and exit
    }
    FileHandle.standardError.write(Data("desktop-via-clyde shim: \(message)\n".utf8))
    exit(exitCode)
}

func tcpReachable(host: String, port: UInt16, timeoutMs: Int) -> Bool {
    var hints = addrinfo(
        ai_flags: 0,
        ai_family: AF_UNSPEC,
        ai_socktype: SOCK_STREAM,
        ai_protocol: IPPROTO_TCP,
        ai_addrlen: 0,
        ai_canonname: nil,
        ai_addr: nil,
        ai_next: nil
    )
    var result: UnsafeMutablePointer<addrinfo>?
    let status = getaddrinfo(host, String(port), &hints, &result)
    if status != 0 || result == nil {
        return false
    }
    defer { freeaddrinfo(result) }
    var info = result
    while let current = info {
        let fileDescriptor = socket(current.pointee.ai_family, current.pointee.ai_socktype, current.pointee.ai_protocol)
        if fileDescriptor >= 0 {
            var flags = fcntl(fileDescriptor, F_GETFL, 0)
            _ = fcntl(fileDescriptor, F_SETFL, flags | O_NONBLOCK)
            let connectResult = connect(fileDescriptor, current.pointee.ai_addr, current.pointee.ai_addrlen)
            if connectResult == 0 {
                close(fileDescriptor)
                return true
            }
            if errno == EINPROGRESS {
                var pollDescriptor = pollfd(fd: fileDescriptor, events: Int16(POLLOUT), revents: 0)
                let ready = poll(&pollDescriptor, 1, Int32(timeoutMs))
                if ready > 0 {
                    var socketError: Int32 = 0
                    var length = socklen_t(MemoryLayout<Int32>.size)
                    getsockopt(fileDescriptor, SOL_SOCKET, SO_ERROR, &socketError, &length)
                    close(fileDescriptor)
                    if socketError == 0 {
                        return true
                    }
                } else {
                    close(fileDescriptor)
                }
            } else {
                close(fileDescriptor)
            }
            flags = 0
        }
        info = current.pointee.ai_next
    }
    return false
}

func runPreflight(_ config: ShimConfig) {
    if !FileManager.default.fileExists(atPath: config.caCertificate) {
        showAlertAndExit("CA certificate missing at \(config.caCertificate). Start clyde first.", 11)
    }
    if !tcpReachable(host: config.proxyHost, port: config.proxyPort, timeoutMs: 500) {
        showAlertAndExit("Cannot reach clyde MITM proxy at [\(config.proxyHost)]:\(config.proxyPort). Start clyde first.", 12)
    }
}

func targetKey(argv0: String, selfPath: String) -> String {
    let executableName = argv0.lowercased()
    let executablePath = selfPath.lowercased()
    if executableName == "codex" || executablePath.contains("/codex.app/") {
        return "codex"
    }
    if executableName == "cursor" || executablePath.contains("/cursor.app/") {
        return "cursor"
    }
    if executableName == "claude" || executablePath.contains("/claude.app/") {
        return "claude"
    }
    return "cursor"
}

func targetPolicy(argv0: String, selfPath: String, config: ShimConfig) -> TargetPolicy {
    return config.targetPolicies[targetKey(argv0: argv0, selfPath: selfPath)] ?? .default
}

func envActions(policy: TargetPolicy, config: ShimConfig) -> [EnvAction] {
    let proxy = proxyURL(config)
    switch policy {
    case .codex:
        return [
            EnvAction(key: "CODEX_CA_CERTIFICATE", value: config.caCertificate),
            EnvAction(key: "NODE_EXTRA_CA_CERTS", value: config.caCertificate),
            EnvAction(key: "NODE_OPTIONS", value: "--use-openssl-ca"),
            EnvAction(key: "NODE_TLS_REJECT_UNAUTHORIZED", value: "0"),
            EnvAction(key: "HTTPS_PROXY", value: proxy),
            EnvAction(key: "HTTP_PROXY", value: proxy),
            EnvAction(key: "ALL_PROXY", value: proxy),
            EnvAction(key: "NO_PROXY", value: config.noProxy),
            EnvAction(key: "no_proxy", value: config.noProxy),
            EnvAction(key: "SSL_CERT_FILE", value: nil),
        ]
    case .default:
        return [
            EnvAction(key: "NODE_EXTRA_CA_CERTS", value: config.caCertificate),
            EnvAction(key: "SSL_CERT_FILE", value: config.caCertificate),
            EnvAction(key: "NODE_OPTIONS", value: "--use-openssl-ca"),
            EnvAction(key: "NODE_TLS_REJECT_UNAUTHORIZED", value: "0"),
            EnvAction(key: "HTTPS_PROXY", value: proxy),
            EnvAction(key: "HTTP_PROXY", value: proxy),
            EnvAction(key: "ALL_PROXY", value: proxy),
            EnvAction(key: "NO_PROXY", value: config.noProxy),
            EnvAction(key: "no_proxy", value: config.noProxy),
        ]
    }
}

func setEnvVars(policy: TargetPolicy, config: ShimConfig) {
    for action in envActions(policy: policy, config: config) {
        if let value = action.value {
            setenv(action.key, value, 1)
        } else {
            unsetenv(action.key)
        }
    }
}

func isElectronRunAsNode() -> Bool {
    ProcessInfo.processInfo.environment["ELECTRON_RUN_AS_NODE"] == "1"
}

func proxyArguments(electronRunAsNode: Bool, config: ShimConfig) -> [String] {
    if electronRunAsNode {
        return []
    }
    return ["--proxy-server=\(proxyURL(config))", "--ignore-certificate-errors"]
}

func main() {
    guard let selfPath = resolveSelfPath() else {
        showAlertAndExit("Could not resolve own executable path", 14)
    }
    let target = selfPath + ".real"
    let argv0 = (selfPath as NSString).lastPathComponent
    let shimConfig: ShimConfig
    do {
        shimConfig = try loadShimConfig()
    } catch {
        showAlertAndExit("Could not load config: \(error.localizedDescription)", 10)
    }
    let policy = targetPolicy(argv0: argv0, selfPath: selfPath, config: shimConfig)

    let args = CommandLine.arguments
    let forwarded = Array(args.dropFirst())
    let electronRunAsNode = isElectronRunAsNode()

    if forwarded.contains("--clyde-dry-run") {
        setEnvVars(policy: policy, config: shimConfig)
        let injected = proxyArguments(electronRunAsNode: electronRunAsNode, config: shimConfig)
        let passThrough = forwarded.filter { $0 != "--clyde-dry-run" }
        let newArgv = [argv0] + injected + passThrough
        print("would exec \(argv0).real")
        print("self: \(selfPath)")
        print("target: \(target)")
        print("argv0: \(argv0)")
        print("target-policy: \(policy.rawValue)")
        print("electron-run-as-node: \(electronRunAsNode)")
        print("launch-cwd: \(shimConfig.launchWorkingDirectory)")
        print("argv: \(newArgv)")
        for action in envActions(policy: policy, config: shimConfig) {
            if let value = action.value {
                print("env \(action.key)=\(value)")
            } else {
                print("env \(action.key)=<unset>")
            }
        }
        exit(0)
    }

    runPreflight(shimConfig)
    setEnvVars(policy: policy, config: shimConfig)
    let launchCwd = shimConfig.launchWorkingDirectory
    if launchCwd.isEmpty {
        showAlertAndExit("Could not resolve launch working directory", 15)
    }
    let chdirResult = launchCwd.withCString { pathPointer in
        chdir(pathPointer)
    }
    if chdirResult != 0 {
        let err = String(cString: strerror(errno))
        showAlertAndExit("Could not change working directory to \(launchCwd): \(err)", 16)
    }

    let injected = proxyArguments(electronRunAsNode: electronRunAsNode, config: shimConfig)
    let newArgv = [argv0] + injected + forwarded

    let cArgs: [UnsafeMutablePointer<CChar>?] = newArgv.map { strdup($0) }
    var cArgsWithNull = cArgs
    cArgsWithNull.append(nil)
    let result = target.withCString { pathPointer in
        execv(pathPointer, cArgsWithNull)
    }
    let err = String(cString: strerror(errno))
    FileHandle.standardError.write(Data("desktop-via-clyde shim: execv(\(target)) failed: \(err)\n".utf8))
    exit(result == 0 ? 13 : 13)
}

main()
