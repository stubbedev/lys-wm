import sys
import traceback

reported: list[BaseException] = []


def report_failure():
    failure = sys.exception()
    if failure is None or any(seen is failure for seen in reported):
        return
    reported.append(failure)
    traceback.print_exception(failure)


def run_script(path):
    with open(path) as source:
        code = compile(source.read(), path, "exec")
    try:
        exec(code, globals())
    except Exception:
        report_failure()
        raise
