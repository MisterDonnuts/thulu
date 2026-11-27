# pytest plugin thulu-gates loads with -p: one JSON line per test that starts or ends, appended to
# $THULU_GATES_PROGRESS, so a run cut short by a hang still says which test never finished. The
# id is pytest's own node id, the one --deselect takes.

import json
import os

_OUT = os.environ.get("THULU_GATES_PROGRESS")


def _write(line):
    if not _OUT:
        return
    try:
        with open(_OUT, "a", encoding="utf-8") as f:
            f.write(json.dumps(line) + "\n")
    except OSError:
        pass


def pytest_runtest_logstart(nodeid, location):
    _write({"event": "start", "file": location[0], "name": nodeid, "nodeid": True})


def pytest_runtest_logreport(report):
    if report.when == "call" or report.failed or (report.when == "setup" and report.skipped):
        status = "fail" if report.failed else "skip" if report.skipped else "pass"
        message = ""
        if report.failed:
            message = str(getattr(report, "longreprtext", "") or "").strip().splitlines()[-1:] or [""]
            message = message[0]
        _write({"event": "end", "file": report.location[0], "name": report.nodeid, "nodeid": True, "status": status, "message": message, "line": (report.location[1] or 0) + 1})
