import Darwin
import Foundation

struct EnvAction: Decodable {
    let action: String
    let key: String
    let value: String?
}

struct ArgAction: Decodable {
    let action: String
    let value: String
}

struct Preflight: Decodable {
    let kind: String
    let path: String?
    let host: String?
    let port: UInt16?
    let timeoutMs: Int?

    enum CodingKeys: String, CodingKey {
        case kind
        case path
        case host
        case port
        case timeoutMs = "timeout_ms"
    }
}

struct LaunchPolicy: Decodable {
    let proxyHost: String
    let proxyPort: UInt16
    let caCertificate: String
    let noProxy: String
    let launchWorkingDirectory: String
    let ignoreDryRunSignal: String?
    let environment: [EnvAction]
    let arguments: [ArgAction]
    let preflights: [Preflight]

    enum CodingKeys: String, CodingKey {
        case proxyHost = "proxy_host"
        case proxyPort = "proxy_port"
        case caCertificate = "ca_certificate"
        case noProxy = "no_proxy"
        case launchWorkingDirectory = "launch_working_directory"
        case ignoreDryRunSignal = "ignore_dry_run_signal"
        case environment
        case arguments
        case preflights
    }
}

func resolveSelfPath() -> String? {
    var size: UInt32 = 0
    _ = _NSGetExecutablePath(nil, &size)
    var buffer = [CChar](repeating: 0, count: Int(size) + 1)
    let rc = _NSGetExecutablePath(&buffer, &size)
    if rc != 0 {
        return nil
    }
    let raw = String(cString: buffer)
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
    }
    FileHandle.standardError.write(Data("desktop-via-clyde shim: \(message)\n".utf8))
    exit(exitCode)
}

func loadLaunchPolicy(selfPath: String) throws -> LaunchPolicy {
    let path = selfPath + ".launch-policy.json"
    guard FileManager.default.fileExists(atPath: path) else {
        throw NSError(domain: "desktop-via-clyde", code: 1, userInfo: [
            NSLocalizedDescriptionKey: "Launch policy missing at \(path). Re-patch the app.",
        ])
    }
    let data = try Data(contentsOf: URL(fileURLWithPath: path))
    return try JSONDecoder().decode(LaunchPolicy.self, from: data)
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

func runPreflights(_ policy: LaunchPolicy) {
    for preflight in policy.preflights {
        switch preflight.kind {
        case "file_exists":
            guard let path = preflight.path else {
                showAlertAndExit("Launch policy file_exists check is missing a path.", 11)
            }
            if !FileManager.default.fileExists(atPath: path) {
                showAlertAndExit("Required file missing at \(path). Start clyde first.", 11)
            }
        case "tcp_reachable":
            guard let host = preflight.host, let port = preflight.port else {
                showAlertAndExit("Launch policy tcp_reachable check is incomplete.", 12)
            }
            let timeoutMs = preflight.timeoutMs ?? 500
            if !tcpReachable(host: host, port: port, timeoutMs: timeoutMs) {
                showAlertAndExit("Cannot reach proxy at [\(host)]:\(port). Start clyde first.", 12)
            }
        default:
            showAlertAndExit("Unsupported launch policy preflight \(preflight.kind). Re-patch the app.", 13)
        }
    }
}

func resolvedEnvActions(policy: LaunchPolicy) -> [EnvAction] {
    return policy.environment
}

func applyEnvironment(policy: LaunchPolicy) {
    for action in resolvedEnvActions(policy: policy) {
        switch action.action {
        case "set":
            if let value = action.value {
                setenv(action.key, value, 1)
            } else {
                unsetenv(action.key)
            }
        case "unset":
            unsetenv(action.key)
        default:
            showAlertAndExit("Unsupported launch policy environment action \(action.action). Re-patch the app.", 14)
        }
    }
}

func isElectronRunAsNode() -> Bool {
    return ProcessInfo.processInfo.environment["ELECTRON_RUN_AS_NODE"] == "1"
}

func resolvedArguments(policy: LaunchPolicy, electronRunAsNode: Bool) -> [String] {
    if electronRunAsNode {
        return []
    }
    var prepended: [String] = []
    var appended: [String] = []
    for action in policy.arguments {
        switch action.action {
        case "prepend":
            prepended.append(action.value)
        case "append":
            appended.append(action.value)
        default:
            showAlertAndExit("Unsupported launch policy argument action \(action.action). Re-patch the app.", 15)
        }
    }
    return prepended + appended
}

func main() {
    guard let selfPath = resolveSelfPath() else {
        showAlertAndExit("Could not resolve own executable path.", 16)
    }
    let target = selfPath + ".real"
    let argv0 = (selfPath as NSString).lastPathComponent
    let policy: LaunchPolicy
    do {
        policy = try loadLaunchPolicy(selfPath: selfPath)
    } catch {
        showAlertAndExit("Could not load launch policy: \(error.localizedDescription)", 10)
    }

    let forwarded = Array(CommandLine.arguments.dropFirst())
    let electronRunAsNode = isElectronRunAsNode()
    if forwarded.contains("--clyde-dry-run") {
        applyEnvironment(policy: policy)
        let injected = resolvedArguments(policy: policy, electronRunAsNode: electronRunAsNode)
        let passThrough = forwarded.filter { $0 != "--clyde-dry-run" }
        let argv = [argv0] + injected + passThrough
        print("would exec \(argv0).real")
        print("self: \(selfPath)")
        print("target: \(target)")
        print("policy: \(selfPath).launch-policy.json")
        print("electron-run-as-node: \(electronRunAsNode)")
        print("launch-cwd: \(policy.launchWorkingDirectory)")
        print("argv: \(argv)")
        for action in resolvedEnvActions(policy: policy) {
            switch action.action {
            case "set":
                print("env \(action.key)=\(action.value ?? "")")
            case "unset":
                print("env \(action.key)=<unset>")
            default:
                print("env \(action.key)=<invalid action \(action.action)>")
            }
        }
        exit(0)
    }

    runPreflights(policy)
    applyEnvironment(policy: policy)
    let launchCwd = policy.launchWorkingDirectory
    let chdirResult = launchCwd.withCString { pointer in
        chdir(pointer)
    }
    if chdirResult != 0 {
        let err = String(cString: strerror(errno))
        showAlertAndExit("Could not change working directory to \(launchCwd): \(err)", 17)
    }

    let injected = resolvedArguments(policy: policy, electronRunAsNode: electronRunAsNode)
    let argv = [argv0] + injected + forwarded
    let cArgs: [UnsafeMutablePointer<CChar>?] = argv.map { strdup($0) }
    var cArgsWithNull = cArgs
    cArgsWithNull.append(nil)
    let result = target.withCString { pathPointer in
        execv(pathPointer, cArgsWithNull)
    }
    let err = String(cString: strerror(errno))
    FileHandle.standardError.write(Data("desktop-via-clyde shim: execv(\(target)) failed: \(err)\n".utf8))
    exit(result == 0 ? 18 : 18)
}

main()
