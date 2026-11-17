"""Errors shared by the task queue and the task handlers. A module of its own,
importing nothing, so that a handler module can be imported before the queue."""


class Permanent(Exception):
    """Raised by a handler for an error another attempt cannot fix (an
    unreadable file): the task is failed at once."""
