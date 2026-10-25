Each clean API area gets its own JSON configuration through the warning-sync
workflow. Files contain an area issue number mapped to sorted operation IDs.
Separate files let independent strict-mode PRs merge without overwriting one
another. The configuration is embedded in the generator; TestStrictAPIAreas
calls go-oapifly's Check for production and optional routes.

The root strict_areas.json is the initial empty configuration. Do not remove an
enabled area to silence a warning; fix the reported contract gap.
