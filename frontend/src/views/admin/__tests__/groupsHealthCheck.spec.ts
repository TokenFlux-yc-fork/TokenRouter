import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const currentDir = dirname(fileURLToPath(import.meta.url));
const groupsViewSource = readFileSync(resolve(currentDir, "../GroupsView.vue"), "utf8");
const groupsAPISource = readFileSync(
  resolve(currentDir, "../../../api/admin/groups.ts"),
  "utf8",
);

describe("group health settings", () => {
  it("keeps health configuration and status wired through the group form", () => {
    expect(groupsViewSource).toContain("health_check_enabled");
    expect(groupsViewSource).toContain("health_check_interval_sec");
    expect(groupsViewSource).toContain("health_check_failure_threshold");
    expect(groupsViewSource).toContain('key: "health_status"');
    expect(groupsViewSource).toContain("resetHealthCheckFormState(editForm, group)");
  });

  it("exposes the manual active-probe endpoint", () => {
    expect(groupsViewSource).toContain("handleTriggerManualHealthCheck");
    expect(groupsAPISource).toContain("triggerManualCheck");
    expect(groupsAPISource).toContain("/manual-check");
  });
});
