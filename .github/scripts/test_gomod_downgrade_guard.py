import os
import tempfile
import unittest

import gomod_downgrade_guard as g

BASE = """module x

go 1.26

require (
	github.com/conduitio/conduit-commons v0.12.1
	github.com/conduitio/conduit-connector-postgres v0.17.2
	github.com/some/direct v1.4.0
	github.com/some/pre v1.0.0-rc.2
)

require (
	github.com/some/indirect v1.2.0 // indirect
	github.com/conduitio/conduit-connector-protocol v0.11.1 // indirect
)
"""


def sub(**kw):
    out = BASE
    for k, v in kw.items():
        out = out.replace(k, v)
    return out


class Guard(unittest.TestCase):
    def run_guard(self, head, body=None):
        with tempfile.TemporaryDirectory() as d:
            paths = []
            for name, text in (("base", BASE), ("head", head)):
                p = os.path.join(d, name)
                open(p, "w").write(text)
                paths.append(p)
            args = paths
            if body is not None:
                bp = os.path.join(d, "body")
                open(bp, "w").write(body)
                args = paths + ["--body-file", bp]
            return g.main(args)

    def test_same_passes(self):
        self.assertEqual(self.run_guard(BASE), 0)

    def test_upgrade_passes(self):
        self.assertEqual(self.run_guard(sub(**{"v0.12.1": "v0.12.2"})), 0)

    def test_protected_downgrade_fails(self):
        # The #3192 shape: a stale rebase lowers conduit-commons.
        self.assertEqual(self.run_guard(sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})), 1)

    def test_builtin_connector_downgrade_fails(self):
        self.assertEqual(self.run_guard(sub(**{"postgres v0.17.2": "postgres v0.17.1"})), 1)

    def test_indirect_protected_downgrade_fails(self):
        self.assertEqual(self.run_guard(sub(**{"protocol v0.11.1": "protocol v0.11.0"})), 1)

    def test_direct_third_party_downgrade_fails(self):
        self.assertEqual(self.run_guard(sub(**{"direct v1.4.0": "direct v1.3.9"})), 1)

    def test_indirect_third_party_downgrade_ignored(self):
        self.assertEqual(self.run_guard(sub(**{"indirect v1.2.0": "indirect v1.1.0"})), 0)

    def test_release_candidate_to_older_rc_fails(self):
        self.assertEqual(self.run_guard(sub(**{"rc.2": "rc.1"})), 1)

    def test_rc_numeric_ordering_is_not_lexical(self):
        self.assertEqual(self.run_guard(sub(**{"rc.2": "rc.10"})), 0)

    def test_release_beats_prerelease(self):
        self.assertEqual(self.run_guard(sub(**{"v1.0.0-rc.2": "v1.0.0"})), 0)
        self.assertEqual(self.run_guard(sub(**{"direct v1.4.0": "direct v1.4.0-rc.1"})), 1)

    def test_removed_module_is_not_a_downgrade(self):
        self.assertEqual(self.run_guard(sub(**{"\tgithub.com/some/direct v1.4.0\n": ""})), 0)

    def test_waiver_with_reason_passes(self):
        head = sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})
        self.assertEqual(self.run_guard(head, "text\ndeps-downgrade: v0.12.1 breaks the s3 build, see #1\n"), 0)

    def test_waiver_without_reason_fails(self):
        head = sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})
        self.assertEqual(self.run_guard(head, "deps-downgrade:   \n"), 1)

    def test_waiver_mid_sentence_does_not_count(self):
        head = sub(**{"conduit-commons v0.12.1": "conduit-commons v0.11.9"})
        self.assertEqual(self.run_guard(head, "I did not add a deps-downgrade: line, sorry"), 1)

    def test_garbage_go_mod_fails_closed(self):
        self.assertEqual(self.run_guard("not a go.mod"), 2)
        self.assertEqual(self.run_guard("require (\n\tfoo v1.0.0\n"), 2)

    def test_pseudo_versions_order_by_timestamp(self):
        a = "v0.0.0-20260101000000-aaaaaaaaaaaa"
        b = "v0.0.0-20260201000000-bbbbbbbbbbbb"
        self.assertLess(g.version_key(a), g.version_key(b))


if __name__ == "__main__":
    unittest.main()
