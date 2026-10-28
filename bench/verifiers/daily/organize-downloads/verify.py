import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, ok, seed_path  # noqa: E402

TASK = Path(__file__).resolve().parent.name

expected = {
    "invoice-march.pdf": "documents",
    "cv.docx": "documents",
    "meeting-notes.TXT": "documents",
    "budget.xlsx": "documents",
    "contacts.csv": "documents",
    "README.md": "documents",
    "beach.jpg": "images",
    "Screenshot 2024-03-01 at 10.15.png": "images",
    "cat.HEIC": "images",
    "diagram.gif": "images",
    "photos-backup.zip": "archives",
    "project.tar.gz": "archives",
    "old-stuff.7z": "archives",
    "installer.dmg": "",
    "song.mp3": "",
    "backup.tar": "",
    "data.json": "",
}

root = Path("Downloads")
if not root.is_dir():
    fail("Downloads/ is missing")
actual = sorted(str(p.relative_to(root)) for p in root.rglob("*") if p.is_file())
want = sorted(str(Path(folder) / name) for name, folder in expected.items())
expect_equal(actual, want, "files under Downloads/")
for name, folder in expected.items():
    if (root / folder / name).read_bytes() != seed_path(TASK, f"Downloads/{name}").read_bytes():
        fail(f"Downloads/{folder}/{name} content changed")
ok()
