#!/usr/bin/env python3
"""Reject macOS packages whose architecture or deployment version is wrong."""

import plistlib
import struct
import sys
from pathlib import Path


def version(text):
    parts = tuple(int(part) for part in text.split("."))
    return (parts + (0, 0, 0))[:3]


def check_binary(path, arch, maximum):
    data = path.read_bytes()
    magic, cpu, _, _, count, _, _, _ = struct.unpack_from("<8I", data)
    expected_cpu = {"amd64": 0x01000007, "arm64": 0x0100000C}[arch]
    if magic != 0xFEEDFACF or cpu != expected_cpu:
        raise ValueError(f"{path}: expected a 64-bit {arch} Mach-O executable")
    offset = 32
    minimums = []
    for _ in range(count):
        command, size = struct.unpack_from("<2I", data, offset)
        if size < 8 or offset + size > len(data):
            raise ValueError(f"{path}: invalid Mach-O load command")
        if command == 0x32:  # LC_BUILD_VERSION
            platform, minimum = struct.unpack_from("<2I", data, offset + 8)
            if platform != 1:  # PLATFORM_MACOS
                raise ValueError(f"{path}: expected macOS platform")
            minimums.append(minimum)
        elif command == 0x24:  # LC_VERSION_MIN_MACOSX
            minimums.append(struct.unpack_from("<I", data, offset + 8)[0])
        offset += size
    if not minimums:
        raise ValueError(f"{path}: missing minimum macOS version")
    for encoded in minimums:
        minimum = (encoded >> 16, (encoded >> 8) & 255, encoded & 255)
        if minimum > maximum:
            raise ValueError(f"{path}: requires macOS {minimum}, exceeds {maximum}")
        print(f"{path.name}: {arch}, minimum macOS {'.'.join(map(str, minimum))}")


if __name__ == "__main__":
    bundle, arch, target = sys.argv[1:]
    contents = Path(bundle) / "Contents"
    maximum = version(target)
    with (contents / "Info.plist").open("rb") as stream:
        info = plistlib.load(stream)
    if version(info["LSMinimumSystemVersion"]) != maximum:
        raise ValueError("Info.plist minimum version differs from deployment target")
    check_binary(contents / "MacOS" / info["CFBundleExecutable"], arch, maximum)
    check_binary(contents / "Resources" / "aissh", arch, maximum)
