import json
import os
import tempfile
import unittest

import connector_release_gate as g


def comment(body, n, who="r"):
    return {"id": n, "body": body, "created_at": f"2026-10-21T00:00:{n:02d}Z", "user": {"login": who}}


def pr(num, labels=(), comments=()):
    return {"number": num, "title": f"pr{num}", "labels": list(labels), "comments": list(comments)}


class Gate(unittest.TestCase):
    def test_clean_prs_pass(self):
        self.assertEqual(g.evaluate([pr(1), pr(2, comments=[comment("Review: approve @ abcdef1", 1)])]), [])

    def test_do_not_merge_label_fails(self):
        self.assertEqual(len(g.evaluate([pr(1, labels=["do-not-merge"])])), 1)

    def test_unanswered_changes_needed_fails(self):
        # The s3 resume / mysql#232 / postgres#433 shape.
        self.assertEqual(len(g.evaluate([pr(7, comments=[comment("Review: changes-needed @ abcdef1", 1)])])), 1)

    def test_answered_changes_needed_passes(self):
        cs = [comment("Review: changes-needed @ abcdef1", 1), comment("Review: approve @ 1234567", 2)]
        self.assertEqual(g.evaluate([pr(7, comments=cs)]), [])

    def test_later_changes_needed_after_approve_fails(self):
        cs = [comment("Review: approve @ abcdef1", 1), comment("Review: changes-needed @ abcdef1", 2)]
        self.assertEqual(len(g.evaluate([pr(7, comments=cs)])), 1)

    def test_cli_evaluate_exit_codes(self):
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "prs.json")
            json.dump([pr(1, labels=["do-not-merge"])], open(p, "w"))
            self.assertEqual(g.main(["evaluate", p]), 1)
            json.dump([pr(1)], open(p, "w"))
            self.assertEqual(g.main(["evaluate", p]), 0)
            open(p, "w").write("{broken")
            self.assertEqual(g.main(["evaluate", p]), 1)
            self.assertEqual(g.main(["evaluate", os.path.join(d, "absent.json")]), 1)

    def test_collect_with_fake_gh(self):
        calls = []

        def gh(*a):
            calls.append(a)
            path = next(x for x in a[1:] if x.startswith("repos/"))
            if "/compare/" in path:
                return json.dumps({"n": 2, "c": ["s1", "s2"]})
            if path.endswith("/pulls") and "/commits/" in path:
                return json.dumps([{"number": 9}])
            if path.endswith("/pulls/9"):
                return json.dumps({"title": "t", "labels": [{"name": "do-not-merge"}]})
            if path.endswith("/comments"):
                return json.dumps(comment("Review: changes-needed @ abcdef1", 1)) + "\n" + json.dumps(comment("hi", 2)) + "\n"
            raise AssertionError(a)

        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "o.json")
            self.assertEqual(g.main(["collect", "o/r", "v1.0.0", "HEAD", out], gh=gh), 0)
            prs = json.load(open(out))
        self.assertEqual([p["number"] for p in prs], [9])  # deduplicated across commits
        self.assertEqual(len(g.evaluate(prs)), 2)

    def test_truncated_compare_fails_closed(self):
        def gh(*a):
            return json.dumps({"n": 400, "c": ["s"] * 250})

        self.assertEqual(g.main(["collect", "o/r", "v1", "HEAD", "/dev/null"], gh=gh), 1)

    def test_unresolvable_previous_tag_fails_closed(self):
        def gh(*a):
            raise g.GateError("404")

        self.assertEqual(g.main(["collect", "o/r", "v-nope", "HEAD", "/dev/null"], gh=gh), 1)


if __name__ == "__main__":
    unittest.main()
