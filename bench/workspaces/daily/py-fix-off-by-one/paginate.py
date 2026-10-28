"""Pagination helpers for the order history page."""


def page_count(total_items, page_size):
    """Number of pages needed to show total_items with page_size items per page."""
    if page_size <= 0:
        raise ValueError("page_size must be positive")
    return total_items // page_size


def page_items(items, page, page_size):
    """Items shown on page number `page`. Pages are numbered from 1."""
    start = page * page_size
    return items[start:start + page_size]
