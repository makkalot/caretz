import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, ok, seed_path  # noqa: E402

TASK = Path(__file__).resolve().parent.name

photos = Path("photos")
if not photos.is_dir():
    fail("photos/ is missing")
seed = seed_path(TASK, "photos")

expected = {}
for src in sorted(seed.iterdir()):
    name = src.stem + ".jpg" if src.suffix == ".JPEG" else src.name
    expected[name] = src.read_bytes()

actual = {p.name: p.read_bytes() for p in sorted(photos.iterdir()) if p.is_file()}
expect_equal(sorted(actual), sorted(expected), "files in photos/")
for name, data in expected.items():
    if actual[name] != data:
        fail(f"photos/{name} content changed")
ok()
