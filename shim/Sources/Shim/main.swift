import Darwin
import Foundation

// Self-locating desktop-via-clyde MITM shim. Replaces
// <App>.app/Contents/MacOS/<ExecName> for any registered target. Resolves its
// own path with _NSGetExecutablePath, derives the sibling .real binary,
// preflights the clyde MITM proxy, applies the target-specific environment
// policy, and execv's the real binary with launch-mode-appropriate proxy
// settings.

let proxyHost = "::1"
let proxyPort: UInt16 = 48723
let proxyURL = "http://[\(proxyHost)]:\(proxyPort)"
let caCertificateEnv = "DESKTOP_VIA_CLYDE_CA_CERT"
let noProxyValue = "localhost,127.0.0.1,::1,[::1]"

enum TargetPolicy: String {
    case codex
    case `default`
}

struct EnvAction {
    let key: String
    let value: String?
}

func homeDir() -> String {
    if let h = ProcessInfo.processInfo.environment["HOME"], !h.isEmpty {
        return h
    }
    return NSHomeDirectory()
}

func caCertificatePath() -> String {
    if let override = ProcessInfo.processInfo.environment[caCertificateEnv], !override.isEmpty {
        return override
    }
    let xdg = ProcessInfo.processInfo.environment["XDG_STATE_HOME"] ?? ""
    let stateBase: String
    if !xdg.isEmpty {
        stateBase = xdg
    } else {
        stateBase = "\(homeDir())/.local/state"
    }
    return "\(stateBase)/clyde/mitm/ca/clyde-mitm-ca.crt"
}

func launchWorkingDirectory() -> String {
    homeDir()
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
    // realpath to collapse any symlink in the bundle path.
    var resolved = [CChar](repeating: 0, count: Int(PATH_MAX))
    if realpath(raw, &resolved) == nil {
        return raw
    }
    return String(cString: resolved)
}

func showAlertAndExit(_ message: String, _ exitCode: Int32) -> Never {
    let escaped = message.replacingOccurrences(of: "\"", with: "\\\"")
    let script = "display alert \"desktop-via-clyde shim\" message \"\(escaped)\" as critical"
    let p = Process()
    p.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
    p.arguments = ["-e", script]
    p.standardOutput = FileHandle.nullDevice
    p.standardError = FileHandle.nullDevice
    do {
        try p.run()
        p.waitUntilExit()
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
    while let cur = info {
        let fd = socket(cur.pointee.ai_family, cur.pointee.ai_socktype, cur.pointee.ai_protocol)
        if fd >= 0 {
            var flags = fcntl(fd, F_GETFL, 0)
            _ = fcntl(fd, F_SETFL, flags | O_NONBLOCK)
            let connectResult = connect(fd, cur.pointee.ai_addr, cur.pointee.ai_addrlen)
            if connectResult == 0 {
                close(fd)
                return true
            }
            if errno == EINPROGRESS {
                var pfd = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
                let ready = poll(&pfd, 1, Int32(timeoutMs))
                if ready > 0 {
                    var soError: Int32 = 0
                    var len = socklen_t(MemoryLayout<Int32>.size)
                    getsockopt(fd, SOL_SOCKET, SO_ERROR, &soError, &len)
                    close(fd)
                    if soError == 0 {
                        return true
                    }
                } else {
                    close(fd)
                }
            } else {
                close(fd)
            }
            flags = 0
        }
        info = cur.pointee.ai_next
    }
    return false
}

func runPreflight() {
    let ca = caCertificatePath()
    if !FileManager.default.fileExists(atPath: ca) {
        showAlertAndExit("CA certificate missing at \(ca). Start clyde first.", 11)
    }
    if !tcpReachable(host: proxyHost, port: proxyPort, timeoutMs: 500) {
        showAlertAndExit("Cannot reach clyde MITM proxy at [\(proxyHost)]:\(proxyPort). Start clyde first.", 12)
    }
}

func targetPolicy(argv0: String, selfPath: String) -> TargetPolicy {
    let executableName = argv0.lowercased()
    let executablePath = selfPath.lowercased()
    if executableName == "codex" || executablePath.contains("/codex.app/") {
        return .codex
    }
    return .default
}

func envActions(policy: TargetPolicy, ca: String) -> [EnvAction] {
    switch policy {
    case .codex:
        return [
            EnvAction(key: "CODEX_CA_CERTIFICATE", value: ca),
            EnvAction(key: "NODE_EXTRA_CA_CERTS", value: ca),
            EnvAction(key: "NODE_OPTIONS", value: "--use-openssl-ca"),
            EnvAction(key: "NODE_TLS_REJECT_UNAUTHORIZED", value: "0"),
            EnvAction(key: "HTTPS_PROXY", value: proxyURL),
            EnvAction(key: "HTTP_PROXY", value: proxyURL),
            EnvAction(key: "ALL_PROXY", value: proxyURL),
            EnvAction(key: "NO_PROXY", value: noProxyValue),
            EnvAction(key: "no_proxy", value: noProxyValue),
            EnvAction(key: "SSL_CERT_FILE", value: nil),
        ]
    case .default:
        return [
            EnvAction(key: "NODE_EXTRA_CA_CERTS", value: ca),
            EnvAction(key: "SSL_CERT_FILE", value: ca),
            EnvAction(key: "NODE_OPTIONS", value: "--use-openssl-ca"),
            EnvAction(key: "NODE_TLS_REJECT_UNAUTHORIZED", value: "0"),
            EnvAction(key: "HTTPS_PROXY", value: proxyURL),
            EnvAction(key: "HTTP_PROXY", value: proxyURL),
            EnvAction(key: "ALL_PROXY", value: proxyURL),
            EnvAction(key: "NO_PROXY", value: noProxyValue),
            EnvAction(key: "no_proxy", value: noProxyValue),
        ]
    }
}

func setEnvVars(policy: TargetPolicy) {
    let ca = caCertificatePath()
    for action in envActions(policy: policy, ca: ca) {
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

func proxyArguments(electronRunAsNode: Bool) -> [String] {
    if electronRunAsNode {
        return []
    }
    return ["--proxy-server=\(proxyURL)", "--ignore-certificate-errors"]
}

func main() {
    guard let selfPath = resolveSelfPath() else {
        showAlertAndExit("Could not resolve own executable path", 14)
    }
    let target = selfPath + ".real"
    let argv0 = (selfPath as NSString).lastPathComponent
    let policy = targetPolicy(argv0: argv0, selfPath: selfPath)

    let args = CommandLine.arguments
    let forwarded = Array(args.dropFirst())
    let electronRunAsNode = isElectronRunAsNode()

    if forwarded.contains("--clyde-dry-run") {
        setEnvVars(policy: policy)
        let injected = proxyArguments(electronRunAsNode: electronRunAsNode)
        let passThrough = forwarded.filter { $0 != "--clyde-dry-run" }
        let newArgv = [argv0] + injected + passThrough
        print("would exec \(argv0).real")
        print("self: \(selfPath)")
        print("target: \(target)")
        print("argv0: \(argv0)")
        print("target-policy: \(policy.rawValue)")
        print("electron-run-as-node: \(electronRunAsNode)")
        print("launch-cwd: \(launchWorkingDirectory())")
        print("argv: \(newArgv)")
        for action in envActions(policy: policy, ca: caCertificatePath()) {
            if let value = action.value {
                print("env \(action.key)=\(value)")
            } else {
                print("env \(action.key)=<unset>")
            }
        }
        exit(0)
    }

    runPreflight()
    setEnvVars(policy: policy)
    let launchCwd = launchWorkingDirectory()
    if launchCwd.isEmpty {
        showAlertAndExit("Could not resolve launch working directory", 15)
    }
    let chdirResult = launchCwd.withCString { pathPtr in
        chdir(pathPtr)
    }
    if chdirResult != 0 {
        let err = String(cString: strerror(errno))
        showAlertAndExit("Could not change working directory to \(launchCwd): \(err)", 16)
    }

    let injected = proxyArguments(electronRunAsNode: electronRunAsNode)
    let newArgv = [argv0] + injected + forwarded

    // Build argv as null-terminated C array for execv.
    let cArgs: [UnsafeMutablePointer<CChar>?] = newArgv.map { strdup($0) }
    var cArgsWithNull = cArgs
    cArgsWithNull.append(nil)
    let rc = target.withCString { pathPtr in
        execv(pathPtr, cArgsWithNull)
    }
    // execv only returns on error.
    let err = String(cString: strerror(errno))
    FileHandle.standardError.write(Data("desktop-via-clyde shim: execv(\(target)) failed: \(err)\n".utf8))
    exit(rc == 0 ? 13 : 13)
}

main()
