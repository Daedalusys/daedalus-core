#!/usr/bin/env python3
"""最小 SMTP 抓取器：把每封邮件原样落到 <outdir>/NNN.eml，stdout 打一行摘要。

只为本机验收用：不投递、不鉴权、明文。启动后监听 127.0.0.1:<port>。
"""
import os
import smtpd
import sys
import time
import asyncore

outdir = sys.argv[1]
port = int(sys.argv[2]) if len(sys.argv) > 2 else 2525
os.makedirs(outdir, exist_ok=True)
seq = {"n": 0}


class Sink(smtpd.SMTPServer):
    def process_message(self, peer, mailfrom, rcpttos, data, **kw):
        seq["n"] += 1
        n = seq["n"]
        path = os.path.join(outdir, "%03d.eml" % n)
        with open(path, "wb") as f:
            f.write(data)
        print("%d rcpt=%s bytes=%d file=%s" % (n, ",".join(rcpttos), len(data), path), flush=True)
        return None


Sink(("127.0.0.1", port), None)
print("listening 127.0.0.1:%d -> %s" % (port, outdir), flush=True)
asyncore.loop()
