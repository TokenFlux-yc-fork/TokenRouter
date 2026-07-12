import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const currentDir = dirname(fileURLToPath(import.meta.url));
const groupsViewSource = readFileSync(
  resolve(currentDir, "../GroupsView.vue"),
  "utf8",
);

describe("groups backup pool settings", () => {
  it("keeps backup pool fields wired through edit normalization and payloads", () => {
    expect(groupsViewSource).toContain("backup_pool_group_id");
    expect(groupsViewSource).toContain("backup_pool_refill_threshold_points");
    expect(groupsViewSource).toContain("isBackupPoolGroupSelectableForEdit");
    expect(groupsViewSource).toContain(
      "const refreshedActiveGroups = await loadUnavailableFallbackGroups();",
    );
    expect(groupsViewSource).toContain("refreshedActiveGroups === null");
    expect(groupsViewSource).toContain('group.platform === "openai"');
    expect(groupsViewSource).toContain(
      "group.backup_pool_refill_threshold_points > 0",
    );
    expect(groupsViewSource).toContain("editForm.backup_pool_group_id = null");
    expect(groupsViewSource).toContain(
      'editForm.platform === "openai" && editForm.backup_pool_group_id !== null',
    );
  });
});
