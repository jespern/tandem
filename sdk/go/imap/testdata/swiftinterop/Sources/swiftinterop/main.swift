import Foundation
import TandemIMAP

// build <channel> <hex envelope>: the message Swift appends, on stdout.
// decode: reads a message on stdin and prints "<channel>\n<hex envelope>", or exits 1 if it doesn't decode.
func hex(_ b: [UInt8]) -> String { b.map { String(format: "%02x", $0) }.joined() }
func unhex(_ s: String) -> [UInt8] {
    var out: [UInt8] = [], i = s.startIndex
    while i < s.endIndex { let j = s.index(i, offsetBy: 2); out.append(UInt8(s[i..<j], radix: 16)!); i = j }
    return out
}

let args = CommandLine.arguments
switch args.count > 1 ? args[1] : "" {
case "build":
    let raw = try MessageFormat.build(channel: args[2], envelope: unhex(args.count > 3 ? args[3] : ""))
    FileHandle.standardOutput.write(Data(raw))
case "decode":
    let raw = [UInt8](FileHandle.standardInput.readDataToEndOfFile())
    let cut = (0..<max(raw.count - 3, 0)).first { raw[$0] == 13 && raw[$0 + 1] == 10 && raw[$0 + 2] == 13 && raw[$0 + 3] == 10 }!
    do {
        let (channel, envelope) = try MessageFormat.decode(header: Array(raw[..<(cut + 4)]), text: Array(raw[(cut + 4)...]))
        print(channel); print(hex(envelope))
    } catch { FileHandle.standardError.write(Data("\(error)\n".utf8)); exit(1) }
default:
    FileHandle.standardError.write(Data("usage: swiftinterop build <channel> <hex> | decode\n".utf8)); exit(2)
}
