import unittest

from paginate import page_count, page_items


class PaginateTest(unittest.TestCase):
    def test_page_count(self):
        self.assertEqual(page_count(10, 3), 4)
        self.assertEqual(page_count(9, 3), 3)
        self.assertEqual(page_count(1, 10), 1)
        self.assertEqual(page_count(0, 3), 0)

    def test_page_count_rejects_bad_size(self):
        with self.assertRaises(ValueError):
            page_count(5, 0)

    def test_page_items(self):
        items = list(range(10))
        self.assertEqual(page_items(items, 1, 3), [0, 1, 2])
        self.assertEqual(page_items(items, 2, 3), [3, 4, 5])
        self.assertEqual(page_items(items, 4, 3), [9])
        self.assertEqual(page_items(items, 5, 3), [])

    def test_every_item_appears_once(self):
        items = list(range(23))
        pages = [page_items(items, p, 5) for p in range(1, page_count(23, 5) + 1)]
        self.assertEqual(sum(pages, []), items)


if __name__ == "__main__":
    unittest.main()
