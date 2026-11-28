import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from check_mobile_admin_coverage import (
    admin_operations,
    coverage_errors,
    generated_routes,
    route_key,
)


class MobileAdminCoverageTest(unittest.TestCase):
    def test_omitted_generated_method_is_reported(self):
        document = {
            "paths": {
                "/api/v1/admin/domains": {"get": {"operationId": "listDomains"}},
                "/api/v1/admin/domains/{domain_id}": {
                    "get": {"operationId": "getDomain"}
                },
                "/api/v1/admin/test/reset": {"post": {"operationId": "resetTestData"}},
            }
        }
        server = admin_operations(document)
        self.assertEqual(len(server), 2)
        with tempfile.TemporaryDirectory() as tmp:
            library = Path(tmp)
            (library / "src/api_client").mkdir(parents=True)
            (library / "src/operation").mkdir()
            (library / "restmail_generated.dart").write_text(
                "export 'src/api_client/admin_domains_api.dart';\n"
            )
            for name, parts in (
                ("list_domains", "r'api', r'v1', r'admin', r'domains'"),
                (
                    "get_domain",
                    (
                        "r'api', r'v1', r'admin', r'domains', "
                        "domainId.toSimple(explode: false, allowEmpty: false)"
                    ),
                ),
            ):
                (library / f"src/operation/{name}.dart").write_text(
                    "        method: 'GET',\n"
                    f"  List<String> _path() {{\n    return [{parts}];\n  }}\n"
                )
            api_file = library / "src/api_client/admin_domains_api.dart"
            api_class = (
                "import 'package:restmail_generated/src/operation/list_domains.dart' as _i2;\n"
                "import 'package:restmail_generated/src/operation/get_domain.dart' as _i3;\n"
                "class AdminDomainsApi {\n"
                "  final _i2.ListDomains _listDomains;\n"
                "  final _i3.GetDomain _getDomain;\n"
                "  Future<Result> getDomain({int? domainId}) async => _getDomain(domainId: domainId);\n"
            )
            list_method = "  Future<Result> listDomains({int? limit}) async => _listDomains(limit: limit);\n"
            missing = ["missing mobile method: listDomains (GET /api/v1/admin/domains)"]
            for methods, expected in (
                (list_method, []),
                ("", missing),
                (
                    list_method.replace(" listDomains(", " _listDomainsPrivate("),
                    missing,
                ),
            ):
                api_file.write_text(api_class + methods + "}\n")
                routes = generated_routes(library)
                mobile = {op for op in server if route_key(op[0], op[1]) in routes}
                self.assertEqual(coverage_errors(server, mobile, {}), expected)

    def test_exclusions_require_a_reason_and_a_current_gap(self):
        op = ("get", "/api/v1/admin/domains", "listDomains")
        self.assertEqual(coverage_errors({op}, set(), {"listDomains": "Known gap"}), [])
        self.assertEqual(
            coverage_errors({op}, set(), {"listDomains": " "}),
            ["exclusion for listDomains needs a reason"],
        )
        self.assertEqual(
            coverage_errors({op}, {op}, {"listDomains": "Known gap"}),
            ["remove stale mobile coverage exclusion: listDomains"],
        )


if __name__ == "__main__":
    unittest.main()
