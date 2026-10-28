import datetime
import sys
import tarfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, ok, require_file, run, workspace_copy  # noqa: E402

TASK = Path(__file__).resolve().parent.name

SOURCE = {
    "a.txt": "alpha\n",
    "nested/deep/b.md": "# bravo\n",
    "with space/c d.txt": "charlie\n",
    "keep.tmpl": "not a temp file\n",
    "x.tmp": "scratch\n",
    "nested/y.tmp": "scratch\n",
}
KEPT = {name: data for name, data in SOURCE.items() if not name.endswith(".tmp")}


def archive_files(path):
    files = {}
    with tarfile.open(path, "r:gz") as tar:
        for member in tar.getmembers():
            name = member.name
            while name.startswith("./"):
                name = name[2:]
            # macOS bsdtar stores extended attributes as AppleDouble "._*" entries.
            if not member.isfile() or Path(name).name.startswith("._"):
                continue
            data = tar.extractfile(member)
            files[name] = data.read().decode(errors="replace") if data else ""
    return files


def check_archive(work, dest, date, env):
    proc = run(["./backup.sh", str(work / "src"), str(dest)], work, env=env)
    if proc.returncode != 0:
        fail(f"backup.sh exited {proc.returncode}\n{proc.stdout}{proc.stderr}")
    archive = dest / f"backup-{date}.tar.gz"
    if not archive.is_file():
        found = sorted(p.name for p in dest.iterdir()) if dest.is_dir() else "missing DEST_DIR"
        fail(f"{archive.name} was not created; DEST_DIR has {found}")
    if archive.name not in proc.stdout:
        fail(f"backup.sh should print the archive path, printed {proc.stdout!r}")
    expect_equal(archive_files(archive), KEPT, "archive contents")


def check_rejects(work, args, what):
    dest = work / "out" / "rejected"
    proc = run(["./backup.sh", *args], work)
    if proc.returncode == 0:
        fail(f"backup.sh should fail when {what}")
    if not proc.stderr.strip():
        fail(f"backup.sh should print usage to stderr when {what}")
    if dest.exists():
        fail(f"backup.sh created DEST_DIR when {what}")


require_file("backup.sh")
with workspace_copy(TASK) as work:
    for name, data in SOURCE.items():
        path = work / "src" / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(data)

    check_archive(work, work / "out" / "dated" / "new", "2024-01-31", {"BACKUP_DATE": "2024-01-31"})
    today = datetime.date.today().isoformat()
    check_archive(work, work / "out" / "today", today, {"BACKUP_DATE": None})

    check_rejects(work, [], "called with no arguments")
    check_rejects(work, [str(work / "src")], "called with one argument")
    check_rejects(work, [str(work / "src" / "a.txt"), str(work / "out" / "rejected")], "SRC_DIR is a file")
ok()
