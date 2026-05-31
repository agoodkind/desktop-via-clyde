// swift-tools-version:6.3
//
//  Package.swift
//  Shim
//
//  Created by Alexander Goodkind <alex@goodkind.io> on 2026-05-30.
//  Copyright © 2026, all rights reserved.
//

import PackageDescription

let package = Package(
    name: "Shim",
    platforms: [
        .macOS(.v12)
    ],
    targets: [
        .executableTarget(
            name: "Shim",
            path: "Sources/Shim"
        )
    ]
)
