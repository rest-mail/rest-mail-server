# Pulumi secret envelopes can wrap the complete Helm values or one field.
# Return only deployment identity; never return the APNs Secret's inputs.
def plain:
  if type == "object" and
     .["4dabf18193072939515e22adb298388d"] == "1b47061264138c4ac30d75fd1eb44270"
  then .value | plain else . end;
def values_hostname:
  plain | if type == "object" then .mailserver | plain |
    if type == "object" then .hostname | plain else null end
  else null end;
def public_hostname:
  plain | if type == "string" and startswith("https://") then ltrimstr("https://") else null end;
def urn: .urn // .newState.urn;

.steps as $steps
| [$steps[]?
   | select(.newState.type == "kubernetes:helm.sh/v3:Release")
   | select((.newState.inputs.name | plain) == $name)
   | select((.newState.inputs.namespace | plain) == $namespace)][0] as $release
| if $release == null then empty else
    [$steps[]?
     | select(.newState.type == "restmail:deployment:RestMail")
     | select(urn == $release.newState.parent)][0] as $component
    | {
        op: $release.op,
        urn: ($release | urn),
        name: ($release.newState.inputs.name | plain),
        hostname: (($release.newState.inputs.values | values_hostname) //
          ($component.newState.outputs.url | public_hostname)),
        version: ($release.newState.inputs.version | plain),
        targets: (if $component == null then [($release | urn)] else
          [($component | urn)] +
          [$steps[]? | select(.newState.parent == ($component | urn)) | urn]
          | unique
        end)
      }
  end
