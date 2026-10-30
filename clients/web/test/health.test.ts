// Daemon health banner: shown only for warn/fail checks, failures first.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { HealthCheckSchema } from "../src/gen/ycc/v1/ycc_pb";
import { healthBanner } from "../src/features/health/model";

const check = (id: string, label: string, status: string) => create(HealthCheckSchema, { id, label, status });

describe("healthBanner", () => {
  it("stays hidden while every check is ok, pending, or off", () => {
    expect(healthBanner([])).toBeNull();
    expect(healthBanner([check("model:opus", "Model opus", "ok"), check("exa", "Web search (Exa)", "off"), check("disk:/", "Disk space (/)", "pending")])).toBeNull();
  });

  it("names failing checks first and takes the worst tone", () => {
    const b = healthBanner([check("disk:/", "Disk space (/)", "warn"), check("model:opus", "Model opus", "fail"), check("exa", "Web search (Exa)", "ok")]);
    expect(b).toEqual({ tone: "fail", text: "Model opus failing · Disk space (/) low" });
    expect(healthBanner([check("exa", "Web search (Exa)", "warn")])?.tone).toBe("warn");
  });
});
