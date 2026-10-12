import unittest

import check_transcript_provenance as c

T = c.TRANSCRIPTS + "anthropic/m/req.yaml"
M = c.TRANSCRIPTS + "anthropic/m/manifest.yaml"


def doc(sha="aaa", text="out", tokens=10):
    return {"systemPromptSHA256": sha, "turns": [{"completionText": text, "tokensUsed": tokens}]}


class ProvenanceTest(unittest.TestCase):
    def test_hash_only_edit_fails(self):
        f, w = c.analyze({T}, {T: doc("aaa")}, {T: doc("bbb")})
        self.assertEqual(len(f), 1)
        self.assertIn("hash-only", f[0])

    def test_real_recapture_passes(self):
        f, w = c.analyze({T, c.GOLDEN}, {T: doc("aaa", "old")}, {T: doc("bbb", "new")})
        self.assertEqual((f, w), ([], False))

    def test_manifest_only_touch_does_not_silence_golden_warning(self):
        f, w = c.analyze({M, c.GOLDEN}, {M: {"x": 1}}, {M: {"x": 2}})
        self.assertEqual((f, w), ([], True))

    def test_hash_only_touch_does_not_silence_golden_warning(self):
        f, w = c.analyze({T, c.GOLDEN}, {T: doc("aaa")}, {T: doc("bbb")})
        self.assertEqual(len(f), 1)
        self.assertTrue(w)

    def test_golden_alone_warns_and_nothing_changed_is_quiet(self):
        self.assertEqual(c.analyze({c.GOLDEN}, {}, {}), ([], True))
        self.assertEqual(c.analyze(set(), {}, {}), ([], False))

    def test_new_response_silences_warning(self):
        self.assertEqual(c.analyze({T, c.GOLDEN}, {T: None}, {T: doc()}), ([], False))


if __name__ == "__main__":
    unittest.main()
