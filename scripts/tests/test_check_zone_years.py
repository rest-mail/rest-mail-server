"""No Go test may assert a zone's clock in a year whose tzdata rules can still change (#1942)."""

import importlib.util
import io
import subprocess
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "check_zone_years", ROOT / "scripts/check_zone_years.py"
)
check = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check)

# The test #1844 had before tzdata 2026f dropped Cuba's 1 November 2026 change
# (b96170b21, internal/api/imap_search_zone_db_test.go), cut to its shape: the zone
# is set on one line, and the dates it asserts are on the others.
HAVANA_2026 = """package api

// TestIMAPSearchOnADayWithoutMidnight holds the day to its real first second where
// the clock skips midnight: Havana goes from 00:00 to 01:00 on 8 March 2026, and
// back from 01:00 to 00:00 on 1 November 2026, so that day has two midnights.
func TestIMAPSearchOnADayWithoutMidnight(t *testing.T) {
	for _, instant := range []string{
		"2026-11-01T04:00:00Z", // 4: the first 00:00:00 on 1 November
		"2026-11-01T05:00:00Z", // 5: the second 00:00:00 on 1 November
	} {
		session.appendAt(utcInstant(t, instant), "Mon, 1 Jan 2024 00:00:00 +0000")
	}
	setIMAPZoneFixtureZone(t, f, `{"time_zone":"America/Havana"}`, http.StatusOK)
	session.requireSearch("ON 1-Nov-2026", "4 5 6")
}
"""


def found(text, year=2026):
    return check.findings(text, year)


class Findings(unittest.TestCase):
    def test_the_havana_test_before_its_fix_fails(self):
        self.assertEqual(found(HAVANA_2026), [(13, "America/Havana", [2026])])

    def test_the_same_test_on_a_year_that_has_ended_passes(self):
        self.assertEqual(found(HAVANA_2026.replace("2026", "2025")), [])

    def test_the_year_is_the_one_given(self):
        self.assertEqual(found(HAVANA_2026, year=2027), [])

    def test_each_date_form_counts(self):
        for date in (
            '"2027-03-08T05:00:00Z"',
            '"ON 8-Mar-2027"',
            '"Sun, 8 Mar 2027 00:00:00 -0500"',
            '"8 March 2027"',
            '"DTSTART;TZID=America/Havana:20270308T010000"',
            "time.Date(2027, 3, 8, 5, 0, 0, 0, time.UTC)",
        ):
            with self.subTest(date=date):
                text = f'func TestX(t *testing.T) {{\n\tzone := "America/Havana"\n\tat := {date}\n}}\n'
                self.assertIn((2, "America/Havana", [2027]), found(text))

    def test_a_table_row_is_judged_by_its_own_dates(self):
        text = (
            "func TestQuiet(t *testing.T) {\n"
            '\t{"berlin", "Europe/Berlin", "2026-10-25T00:15:00Z"},\n'
            '\t{"lord howe", "Australia/Lord_Howe", "2025-10-04T12:00:00Z"},\n'
            '\t{"cuba", "America/Havana", "2026-11-01T04:00:00Z"},\n'
            "}\n"
        )
        self.assertEqual(found(text), [(4, "America/Havana", [2026])])

    def test_a_zone_without_dates_of_its_own_takes_its_declarations(self):
        text = (
            'var zone = "Australia/Lord_Howe"\n'
            "\n"
            "func TestOther(t *testing.T) {\n"
            '\tat := "2026-10-04T12:00:00Z"\n'
            "}\n"
        )
        self.assertEqual(found(text), [])

    def test_zones_fixed_by_law_may_use_later_dates(self):
        for zone in sorted(check.FIXED_BY_LAW):
            with self.subTest(zone=zone):
                text = f'func TestX(t *testing.T) {{\n\tz, at := "{zone}", "2030-03-08T07:00:00Z"\n}}\n'
                self.assertEqual(found(text), [])

    def test_comments_do_not_count(self):
        text = (
            "func TestX(t *testing.T) {\n"
            "\t// Havana moves back on 1 November 2026; see 2026-11-01.\n"
            '\tzone := "America/Havana" /* not 2026-11-01 either */\n'
            '\tat := "2025-11-02T05:00:00Z" // 2026-11-01\n'
            "}\n"
        )
        self.assertEqual(found(text), [])

    def test_a_url_in_a_string_is_not_a_comment(self):
        text = 'func TestX(t *testing.T) {\n\tget("http://x/America/Havana?at=2026-11-01")\n}\n'
        self.assertEqual(found(text), [(2, "America/Havana", [2026])])

    def test_utc_and_fixed_offsets_are_not_zones(self):
        text = (
            "func TestX(t *testing.T) {\n"
            '\ta := time.Date(2030, 1, 1, 0, 0, 0, 0, time.FixedZone("CET", 3600))\n'
            '\tb, _ := time.LoadLocation("UTC")\n'
            '\tc, _ := time.LoadLocation("Etc/GMT+5")\n'
            "}\n"
        )
        self.assertEqual(found(text), [])

    def test_the_message_names_the_file_line_and_allowed_zones(self):
        with tempfile.TemporaryDirectory() as scratch:
            path = Path(scratch) / "zone_test.go"
            path.write_text(HAVANA_2026)
            output = io.StringIO()
            with redirect_stdout(output):
                code = check.main([str(path), "--year", "2026"])
        self.assertEqual(code, 1)
        text = output.getvalue()
        self.assertIn(f"{path}:13: America/Havana is asserted in 2026", text)
        self.assertIn("a year before 2026", text)
        self.assertIn("America/New_York", text)
        self.assertIn("FIXED_BY_LAW", text)


class Repository(unittest.TestCase):
    def test_the_repository_passes_the_check(self):
        result = subprocess.run(
            ["python3", "-B", "scripts/check_zone_years.py"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_the_check_reads_every_tracked_go_test(self):
        names = {path.name for path in check.tracked_tests(ROOT)}
        self.assertIn("schedule_test.go", names)
        self.assertTrue(all(name.endswith("_test.go") for name in names))

    def test_a_chore_runs_the_check(self):
        self.assertIn(
            "python3 -B scripts/check_zone_years.py",
            (ROOT / "chores.yml").read_text(),
        )


if __name__ == "__main__":
    unittest.main()
