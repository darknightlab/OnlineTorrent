# Real-client check of "qB downloads only the file being played" (no production systems):
# a libtorrent seeder + qbittorrent-nox on 127.0.0.1, then the Go live test
# (internal/edge/fileselect_live_test.go) drives otd's qb.Pool.Seed against that qB and reads
# the result through the edge's Disk — including boundary pieces whose skipped-file bytes are in
# libtorrent's partfile.
#
#   nix build nixpkgs#qbittorrent-nox nixpkgs#python3Packages.libtorrent-rasterbar --no-link --print-out-paths
#   PYTHONPATH=<libtorrent-python>/lib/python3.X/site-packages python3 web/e2e/fileselect/run.py <qbittorrent-nox> [v1|hybrid]
#
# v1: no padding → pieces shared between files (partfile path); hybrid: v1+v2 with BEP 47 padding
# (qB hides the pad files and switches the torrent id to the truncated v2 hash).
import os, random, shutil, subprocess, sys, threading, time, urllib.parse, urllib.request
import libtorrent as lt

QBNOX = sys.argv[1]
MODE = sys.argv[2] if len(sys.argv) > 2 else "v1"
REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "../../.."))
W = f"/tmp/fsel/live-{MODE}-{os.getpid()}"
shutil.rmtree(W, ignore_errors=True)
os.makedirs(f"{W}/seed/Pack")
rnd = random.Random(7)
# odd sizes so file boundaries fall inside pieces (v1)
for n, s in [("ep01.mkv", 700_001), ("ep02.mkv", 1_300_003), ("ep03.mkv", 900_007), ("notes.txt", 12_345)]:
    with open(f"{W}/seed/Pack/{n}", "wb") as f:
        f.write(rnd.randbytes(s))
fs = lt.file_storage()
lt.add_files(fs, f"{W}/seed/Pack")
ct = lt.create_torrent(fs, 65536, flags=lt.create_torrent.v1_only if MODE == "v1" else 0)
lt.set_piece_hashes(ct, f"{W}/seed")
tb = lt.bencode(ct.generate())
open(f"{W}/t.torrent", "wb").write(tb)
ti = lt.torrent_info(lt.bdecode(tb))
ids = {str(ti.info_hashes().v1), str(ti.info_hashes().get_best())}

ses = lt.session({"listen_interfaces": "127.0.0.1:0", "enable_dht": False, "enable_lsd": False, "enable_upnp": False, "enable_natpmp": False})
ses.add_torrent({"ti": ti, "save_path": f"{W}/seed", "flags": lt.torrent_flags.seed_mode})
time.sleep(1)
sport = ses.listen_port()

prof, web = f"{W}/qbprof", random.randint(19000, 29999)
os.makedirs(f"{prof}/qBittorrent/config")
open(f"{prof}/qBittorrent/config/qBittorrent.conf", "w").write(f"""[LegalNotice]
Accepted=true
[BitTorrent]
Session\\DHTEnabled=false
Session\\LSDEnabled=false
Session\\PeXEnabled=false
Session\\Port={random.randint(30000, 40000)}
Session\\DefaultSavePath={W}/qbdata
Session\\Preallocation=false
Session\\AddExtensionToIncompleteFiles=true
Session\\QueueingSystemEnabled=false
[Network]
PortForwardingEnabled=false
Proxy\\Type=None
[Preferences]
WebUI\\Port={web}
WebUI\\Address=127.0.0.1
WebUI\\LocalHostAuth=false
WebUI\\HostHeaderValidation=false
WebUI\\CSRFProtection=false
""")
noproxy = {**os.environ, "http_proxy": "", "https_proxy": "", "HTTP_PROXY": "", "HTTPS_PROXY": ""}
qbp = subprocess.Popen([QBNOX, f"--profile={prof}", "--confirm-legal-notice"], stdout=open(f"{W}/qb.log", "w"), stderr=subprocess.STDOUT, env=noproxy)
B = f"http://127.0.0.1:{web}"
op = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for _ in range(100):
    try:
        op.open(B + "/api/v2/app/version", timeout=2).read()
        break
    except Exception:
        time.sleep(0.2)

stop = False
def peers():  # qB has no tracker / DHT here: keep telling it where the seeder is
    while not stop:
        for h in ids:
            try:
                op.open(urllib.request.Request(B + "/api/v2/torrents/addPeers", data=urllib.parse.urlencode({"hashes": h, "peers": f"127.0.0.1:{sport}"}).encode()), timeout=2).read()
            except Exception:
                pass
        time.sleep(1)
threading.Thread(target=peers, daemon=True).start()
try:
    env = {**noproxy, "QB_URL": B, "LIVE_TORRENT": f"{W}/t.torrent", "LIVE_WANT": "Pack/ep02.mkv", "LIVE_SAVE": f"{W}/qbdata", "LIVE_SEED": f"{W}/seed",
           "LIVE_EXPECT_BOUNDARY": "1" if MODE == "v1" else "0", "GOMAXPROCS": "2"}
    r = subprocess.run(["go", "test", "-tags", "live", "-count=1", "-run", "TestLiveFileSelection", "-v", "./internal/edge"], cwd=REPO, env=env)
    sys.exit(r.returncode)
finally:
    stop = True
    qbp.kill()
