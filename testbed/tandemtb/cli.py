"""`tb`: the Tandem testbed command line. Output is JSON where it's data."""

import argparse
import json
import os
import sys
import unittest

from . import hubops, stalwart, toxi


def emit(obj):
    print(json.dumps(obj, indent=2))


def hub_desc(args):
    if getattr(args, "descriptor", None):
        with open(args.descriptor) as f:
            return json.load(f)
    return stalwart.hub(args.hub)


def main(argv=None):
    p = argparse.ArgumentParser(prog="tb", description="Tandem testbed: Stalwart (JMAP + IMAP) behind Toxiproxy.")
    sub = p.add_subparsers(dest="cmd", required=True)

    sub.add_parser("up", help="bootstrap if needed, then start the testbed")
    sub.add_parser("down", help="stop the testbed (keeps data)")
    sub.add_parser("reset", help="delete containers, volumes and state")
    sub.add_parser("status", help="containers, hubs and proxies")

    h = sub.add_parser("hub", help="hub accounts and hostile-hub operations")
    hs = h.add_subparsers(dest="hcmd", required=True)
    hs.add_parser("new", help="create a fresh hub account").add_argument("--name")
    for name, helptext in (("show", "print a hub's descriptor"), ("delete", "delete a hub account"),
                           ("ls", "list messages in the Tandem folder"),
                           ("rotate-password", "change the account password (hub: reject auth)")):
        sp = hs.add_parser(name, help=helptext)
        sp.add_argument("hub")
    d = hs.add_parser("destroy", help="delete messages (hub: destroy)")
    d.add_argument("hub")
    d.add_argument("--channel")
    d.add_argument("--uid", type=int, action="append")
    d.add_argument("--latest", type=int)
    d.add_argument("--all", action="store_true")
    s = hs.add_parser("snapshot", help="capture raw messages to a directory")
    s.add_argument("hub"); s.add_argument("dir"); s.add_argument("--channel")
    r = hs.add_parser("replay", help="append captured .eml files again (hub: replay)")
    r.add_argument("hub"); r.add_argument("files", nargs="+")
    rb = hs.add_parser("rollback", help="restore a snapshot (hub: rollback)")
    rb.add_argument("hub"); rb.add_argument("dir"); rb.add_argument("--channel")
    mv = hs.add_parser("move", help="move a message out of Tandem (hub: move)")
    mv.add_argument("hub"); mv.add_argument("uid", type=int); mv.add_argument("--to", default="INBOX")
    inj = hs.add_parser("inject", help="append arbitrary bytes (hub: inject)")
    inj.add_argument("hub"); inj.add_argument("file", help="raw RFC 5322 message, or envelope bytes with --channel")
    inj.add_argument("--channel", help="wrap the file's bytes as an envelope in a well-formed message")
    rw = hs.add_parser("rewrite-charset", help="re-encode a body (hub: rewrite charset)")
    rw.add_argument("hub"); rw.add_argument("uid", type=int)
    rw.add_argument("--charset", default="utf-16"); rw.add_argument("--encoding", default="base64",
                                                                      choices=["base64", "quoted-printable", "7bit", "8bit"])

    f = sub.add_parser("fault", help="per-device network faults via Toxiproxy")
    fs = f.add_subparsers(dest="fcmd", required=True)
    pr = fs.add_parser("proxy", help="create a device's proxies; prints its hub descriptor")
    pr.add_argument("device"); pr.add_argument("hub")
    for name, helptext in (("partition", "cut a device off"), ("heal", "undo partition and toxics"),
                           ("blackhole", "keep connections open but pass no data"),
                           ("remove", "delete a device's proxies")):
        fs.add_parser(name, help=helptext).add_argument("device")
    lt = fs.add_parser("latency", help="add latency"); lt.add_argument("device"); lt.add_argument("ms", type=int)
    lt.add_argument("--jitter", type=int, default=0)
    ca = fs.add_parser("cut-after", help="drop the connection after N bytes")
    ca.add_argument("device"); ca.add_argument("bytes", type=int)
    ca.add_argument("--stream", choices=["upstream", "downstream"], default="downstream")
    rp = fs.add_parser("reset-peer", help="TCP reset after a delay"); rp.add_argument("device")
    rp.add_argument("--after-ms", type=int, default=0)
    fs.add_parser("clear", help="reset every proxy and toxic")

    pb = sub.add_parser("probe", help="run the binding conformance probes")
    pb.add_argument("--hub-file", help="probe a real hub described by a JSON descriptor instead of the testbed")
    pb.add_argument("-k", dest="pattern", help="only probes whose name contains this")
    pb.add_argument("-v", action="store_true")

    args = p.parse_args(argv)

    if args.cmd == "up":
        stalwart.up()
    elif args.cmd == "down":
        stalwart.down()
    elif args.cmd == "reset":
        stalwart.reset()
    elif args.cmd == "status":
        state = stalwart.load_state()
        stalwart.compose("ps", capture=False)
        emit({"bootstrapped": bool(state), "hubs": sorted((state or {}).get("hubs", {})),
              "proxies": sorted(toxi.proxies()) if state else []})
    elif args.cmd == "hub":
        if args.hcmd == "new":
            emit(stalwart.new_hub(args.name))
        elif args.hcmd == "show":
            emit(stalwart.hub(args.hub))
        elif args.hcmd == "delete":
            stalwart.delete_hub(args.hub)
        elif args.hcmd == "rotate-password":
            emit(stalwart.set_password(args.hub))
        elif args.hcmd == "ls":
            emit(hubops.listing(stalwart.hub(args.hub)))
        elif args.hcmd == "destroy":
            emit(hubops.destroy(stalwart.hub(args.hub), channel=args.channel, uids=args.uid,
                                latest=args.latest, everything=args.all))
        elif args.hcmd == "snapshot":
            emit(hubops.snapshot(stalwart.hub(args.hub), args.dir, args.channel))
        elif args.hcmd == "replay":
            emit(hubops.replay(stalwart.hub(args.hub), args.files))
        elif args.hcmd == "rollback":
            emit(hubops.rollback(stalwart.hub(args.hub), args.dir, args.channel))
        elif args.hcmd == "move":
            hubops.move(stalwart.hub(args.hub), args.uid, args.to)
        elif args.hcmd == "inject":
            data = open(args.file, "rb").read()
            uid = hubops.inject(stalwart.hub(args.hub), raw=None if args.channel else data,
                                channel=args.channel, envelope=data if args.channel else None)
            emit({"uid": uid})
        elif args.hcmd == "rewrite-charset":
            emit({"uid": hubops.rewrite_charset(stalwart.hub(args.hub), args.uid, args.charset, args.encoding)})
    elif args.cmd == "fault":
        if args.fcmd == "proxy":
            emit(toxi.device_descriptor(args.device, args.hub))
        elif args.fcmd == "partition":
            toxi.partition(args.device)
        elif args.fcmd == "heal":
            toxi.heal(args.device)
        elif args.fcmd == "blackhole":
            toxi.blackhole(args.device)
        elif args.fcmd == "remove":
            toxi.remove(args.device)
        elif args.fcmd == "latency":
            toxi.latency(args.device, args.ms, args.jitter)
        elif args.fcmd == "cut-after":
            toxi.cut_after(args.device, args.bytes, args.stream)
        elif args.fcmd == "reset-peer":
            toxi.reset_peer(args.device, args.after_ms)
        elif args.fcmd == "clear":
            toxi.reset_all()
    elif args.cmd == "probe":
        if args.hub_file:
            os.environ["TB_PROBE_HUB"] = os.path.abspath(args.hub_file)
        loader = unittest.TestLoader()
        if args.pattern:
            loader.testNamePatterns = [f"*{args.pattern}*"]
        suite = loader.discover(str(stalwart.HERE / "probes"), top_level_dir=str(stalwart.HERE))
        result = unittest.TextTestRunner(verbosity=2 if args.v else 1).run(suite)
        return 0 if result.wasSuccessful() else 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
