//
//  main.swift
//  Shim
//
//  Created by Alexander Goodkind <alex@goodkind.io> on 2026-05-30.
//  Copyright © 2026, all rights reserved.
//

import Darwin
import Foundation
import OSLog

// MARK: - Constants

let shimLog = Logger(subsystem: "goodkind.io.desktop-via-clyde", category: "shim")
let launchPolicyMissingExitCode: Int32 = 10
let filePreflightExitCode: Int32 = 11
let tcpPreflightExitCode: Int32 = 12
let unsupportedPreflightExitCode: Int32 = 13
let unsupportedEnvironmentExitCode: Int32 = 14
let unsupportedArgumentExitCode: Int32 = 15
let selfPathFailureExitCode: Int32 = 16
let workingDirectoryFailureExitCode: Int32 = 17
let execFailureExitCode: Int32 = 18
let defaultTCPTimeoutMilliseconds = 500
let successfulSystemCallResult: Int32 = 0
let nullTerminator: CChar = 0
let singlePollDescriptorCount: nfds_t = 1
let missingPolicyErrorCode = 1

// MARK: - EnvAction

struct EnvAction: Decodable {
    let action: String
    let key: String
    let value: String?
}

// MARK: - ArgAction

struct ArgAction: Decodable {
    let action: String
    let value: String
}

// MARK: - Preflight

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

// MARK: - LaunchPolicy

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

// MARK: - LaunchContext

struct LaunchContext {
    let argv0: String
    let selfPath: String
    let target: String
    let policy: LaunchPolicy
    let forwarded: [String]
    let electronRunAsNode: Bool
}

// MARK: - Helpers

func stringFromNullTerminatedBytes(_ bytes: [CChar]) -> String? {
    let terminatorIndex = bytes.firstIndex(of: nullTerminator) ?? bytes.count
    let characters = bytes[..<terminatorIndex].map { UInt8(bitPattern: $0) }
    return String(bytes: characters, encoding: .utf8)
}

func resolveSelfPath() -> String? {
    var size: UInt32 = 0
    _ = _NSGetExecutablePath(nil, &size)
    var buffer = [CChar](repeating: 0, count: Int(size) + 1)
    let rc = _NSGetExecutablePath(&buffer, &size)
    if rc != 0 {
        return nil
    }
    guard let raw = stringFromNullTerminatedBytes(buffer) else {
        return nil
    }
    var resolved = [CChar](repeating: 0, count: Int(PATH_MAX))
    if realpath(raw, &resolved) == nil {
        return raw
    }
    return stringFromNullTerminatedBytes(resolved) ?? raw
}

func showAlertAndExit(_ message: String, _ exitCode: Int32) -> Never {
    shimLog.error("desktop-via-clyde shim exiting: \(message, privacy: .public)")
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
    } catch let alertError as NSError {
        shimLog.warning(
            "desktop-via-clyde shim alert failed: \(alertError.localizedDescription, privacy: .public)"
        )
    }
    FileHandle.standardError.write(Data("desktop-via-clyde shim: \(message)\n".utf8))
    exit(exitCode)
}

func writeDryRunLine(_ line: String) {
    FileHandle.standardOutput.write(Data("\(line)\n".utf8))
}

// launchPolicyPath returns the policy file path for a shim at selfPath. The
// patcher installs the policy in Contents/Resources, because codesign seals
// Resources files as ordinary resources while a non-Mach-O file beside the
// executable in Contents/MacOS breaks the bundle signature. When no Resources
// policy exists the shim falls back to the legacy path beside itself, which
// keeps non-bundle layouts (and tests) working.
func launchPolicyPath(selfPath: String) -> String {
    let macOSDir = (selfPath as NSString).deletingLastPathComponent
    let contentsDir = (macOSDir as NSString).deletingLastPathComponent
    let exeName = (selfPath as NSString).lastPathComponent
    let resourcesDir = (contentsDir as NSString).appendingPathComponent("Resources")
    let policyName = "\(exeName).launch-policy.json"
    let resourcesPolicy = (resourcesDir as NSString).appendingPathComponent(policyName)
    if FileManager.default.fileExists(atPath: resourcesPolicy) {
        return resourcesPolicy
    }
    return selfPath + ".launch-policy.json"
}

func loadLaunchPolicy(selfPath: String) throws -> LaunchPolicy {
    let path = launchPolicyPath(selfPath: selfPath)
    guard FileManager.default.fileExists(atPath: path) else {
        throw NSError(
            domain: "desktop-via-clyde",
            code: missingPolicyErrorCode,
            userInfo: [
                NSLocalizedDescriptionKey: "Launch policy missing at \(path). Re-patch the app."
            ]
        )
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
    if status != successfulSystemCallResult || result == nil {
        return false
    }
    defer { freeaddrinfo(result) }
    var info = result
    while let current = info {
        let fileDescriptor = socket(
            current.pointee.ai_family, current.pointee.ai_socktype, current.pointee.ai_protocol)
        if fileDescriptor >= 0 {
            var flags = fcntl(fileDescriptor, F_GETFL, 0)
            _ = fcntl(fileDescriptor, F_SETFL, flags | O_NONBLOCK)
            let connectResult = connect(
                fileDescriptor, current.pointee.ai_addr, current.pointee.ai_addrlen)
            if connectResult == successfulSystemCallResult {
                close(fileDescriptor)
                return true
            }
            if errno == EINPROGRESS {
                var pollDescriptor = pollfd(fd: fileDescriptor, events: Int16(POLLOUT), revents: 0)
                let ready = poll(&pollDescriptor, singlePollDescriptorCount, Int32(timeoutMs))
                if ready > successfulSystemCallResult {
                    var socketError: Int32 = 0
                    var length = socklen_t(MemoryLayout<Int32>.size)
                    getsockopt(fileDescriptor, SOL_SOCKET, SO_ERROR, &socketError, &length)
                    close(fileDescriptor)
                    if socketError == successfulSystemCallResult {
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
                showAlertAndExit(
                    "Launch policy file_exists check is missing a path.", filePreflightExitCode)
            }
            if !FileManager.default.fileExists(atPath: path) {
                showAlertAndExit(
                    "Required file missing at \(path). Start clyde first.",
                    filePreflightExitCode
                )
            }
        case "tcp_reachable":
            guard let host = preflight.host, let port = preflight.port else {
                showAlertAndExit(
                    "Launch policy tcp_reachable check is incomplete.",
                    tcpPreflightExitCode
                )
            }
            let timeoutMs = preflight.timeoutMs ?? defaultTCPTimeoutMilliseconds
            if !tcpReachable(host: host, port: port, timeoutMs: timeoutMs) {
                showAlertAndExit(
                    "Cannot reach proxy at [\(host)]:\(port). Start clyde first.",
                    tcpPreflightExitCode
                )
            }
        default:
            showAlertAndExit(
                "Unsupported launch policy preflight \(preflight.kind). Re-patch the app.",
                unsupportedPreflightExitCode
            )
        }
    }
}

func resolvedEnvActions(policy: LaunchPolicy) -> [EnvAction] {
    policy.environment
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
            showAlertAndExit(
                "Unsupported launch policy environment action \(action.action). Re-patch the app.",
                unsupportedEnvironmentExitCode
            )
        }
    }
}

func isElectronRunAsNode() -> Bool {
    ProcessInfo.processInfo.environment["ELECTRON_RUN_AS_NODE"] == "1"
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
            showAlertAndExit(
                "Unsupported launch policy argument action \(action.action). Re-patch the app.",
                unsupportedArgumentExitCode
            )
        }
    }
    return prepended + appended
}

// MARK: - Entry Point

func runDryRun(
    context: LaunchContext
) -> Never {
    applyEnvironment(policy: context.policy)
    let injected = resolvedArguments(
        policy: context.policy,
        electronRunAsNode: context.electronRunAsNode
    )
    let passThrough = context.forwarded.filter { $0 != "--clyde-dry-run" }
    let argv = [context.argv0] + injected + passThrough
    writeDryRunLine("would exec \(context.argv0).real")
    writeDryRunLine("self: \(context.selfPath)")
    writeDryRunLine("target: \(context.target)")
    writeDryRunLine("policy: \(launchPolicyPath(selfPath: context.selfPath))")
    writeDryRunLine("electron-run-as-node: \(context.electronRunAsNode)")
    writeDryRunLine("launch-cwd: \(context.policy.launchWorkingDirectory)")
    writeDryRunLine("argv: \(argv)")
    for action in resolvedEnvActions(policy: context.policy) {
        switch action.action {
        case "set":
            writeDryRunLine("env \(action.key)=\(action.value ?? "")")
        case "unset":
            writeDryRunLine("env \(action.key)=<unset>")
        default:
            writeDryRunLine("env \(action.key)=<invalid action \(action.action)>")
        }
    }
    exit(successfulSystemCallResult)
}

func launchRealBinary(
    context: LaunchContext
) -> Never {
    runPreflights(context.policy)
    applyEnvironment(policy: context.policy)
    let launchCwd = context.policy.launchWorkingDirectory
    let chdirResult = launchCwd.withCString { pointer in
        chdir(pointer)
    }
    if chdirResult != successfulSystemCallResult {
        let err = String(cString: strerror(errno))
        showAlertAndExit(
            "Could not change working directory to \(launchCwd): \(err)",
            workingDirectoryFailureExitCode
        )
    }

    let injected = resolvedArguments(
        policy: context.policy,
        electronRunAsNode: context.electronRunAsNode
    )
    let argv = [context.argv0] + injected + context.forwarded
    let cArgs: [UnsafeMutablePointer<CChar>?] = argv.map { strdup($0) }
    var cArgsWithNull = cArgs
    cArgsWithNull.append(nil)
    _ = context.target.withCString { pathPointer in
        execv(pathPointer, cArgsWithNull)
    }
    let err = String(cString: strerror(errno))
    FileHandle.standardError.write(
        Data("desktop-via-clyde shim: execv(\(context.target)) failed: \(err)\n".utf8))
    exit(execFailureExitCode)
}

func main() -> Never {
    guard let selfPath = resolveSelfPath() else {
        showAlertAndExit("Could not resolve own executable path.", selfPathFailureExitCode)
    }
    let target = selfPath + ".real"
    let argv0 = (selfPath as NSString).lastPathComponent
    let policy: LaunchPolicy
    do {
        policy = try loadLaunchPolicy(selfPath: selfPath)
    } catch let policyError as NSError {
        shimLog.error(
            "desktop-via-clyde shim policy load failed: \(policyError.localizedDescription, privacy: .public)"
        )
        showAlertAndExit(
            "Could not load launch policy: \(policyError.localizedDescription)",
            launchPolicyMissingExitCode
        )
    }

    let forwarded = Array(CommandLine.arguments.dropFirst())
    let electronRunAsNode = isElectronRunAsNode()
    let launchContext = LaunchContext(
        argv0: argv0,
        selfPath: selfPath,
        target: target,
        policy: policy,
        forwarded: forwarded,
        electronRunAsNode: electronRunAsNode
    )
    if forwarded.contains("--clyde-dry-run") {
        runDryRun(context: launchContext)
    }

    launchRealBinary(context: launchContext)
}

main()
