import type { components } from "./api-types";

// The server currently describes these JSON columns as unknown (#658). Keep the
// wire types intact and validate the shapes the editor/timeline needs to render.
export interface FilterConfig {
  name: string;
  type: "action" | "transform";
  enabled: boolean;
  unskippable?: boolean;
  config?: Record<string, unknown>;
}
export type TraceStage = components["schemas"]["StepResult"];
export type Pipeline = Omit<components["schemas"]["Pipeline"], "filters"> & {
  filters: FilterConfig[];
};
export type MessageTrace = Omit<
  components["schemas"]["MessageTrace"],
  "stages"
> & { stages: TraceStage[] };
export type CustomFilter = Omit<
  components["schemas"]["CustomFilter"],
  "config"
> & { config: Record<string, unknown> & { script: string } };

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isFilterConfig(value: unknown): value is FilterConfig {
  return (
    isRecord(value) &&
    typeof value.name === "string" &&
    (value.type === "action" || value.type === "transform") &&
    typeof value.enabled === "boolean" &&
    (value.unskippable === undefined ||
      typeof value.unskippable === "boolean") &&
    (value.config === undefined || isRecord(value.config))
  );
}

function isTraceStage(value: unknown): value is TraceStage {
  return (
    isRecord(value) &&
    typeof value.filter_name === "string" &&
    typeof value.filter_type === "string" &&
    typeof value.action === "string" &&
    typeof value.duration_ms === "number" &&
    isRecord(value.log) &&
    typeof value.log.filter === "string" &&
    typeof value.log.result === "string" &&
    (value.log.detail === undefined || typeof value.log.detail === "string") &&
    (value.log.duration_ms === undefined ||
      typeof value.log.duration_ms === "number") &&
    (value.skipped === undefined || typeof value.skipped === "boolean") &&
    ["skip_reason", "error"].every(
      (key) => value[key] === undefined || typeof value[key] === "string",
    )
  );
}

export function pipelineView(
  value: components["schemas"]["Pipeline"],
): Pipeline {
  const filters = value.filters ?? [];
  if (!Array.isArray(filters) || !filters.every(isFilterConfig)) {
    throw new Error("The pipeline has an invalid filter configuration");
  }
  return { ...value, filters };
}

export function traceView(
  value: components["schemas"]["MessageTrace"],
): MessageTrace {
  const stages = value.stages ?? [];
  if (!Array.isArray(stages) || !stages.every(isTraceStage)) {
    throw new Error("The message trace has invalid stages");
  }
  return { ...value, stages };
}

export function customFilterView(
  value: components["schemas"]["CustomFilter"],
): CustomFilter {
  if (!isRecord(value.config) || typeof value.config.script !== "string") {
    throw new Error("The custom filter has an invalid script configuration");
  }
  return { ...value, config: { ...value.config, script: value.config.script } };
}
