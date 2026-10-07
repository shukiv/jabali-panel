// GH #1993: an upload restore asks to overwrite only when the admin (or the
// tenant) checked "Overwrite existing items with the backup"; otherwise the
// server keeps what the account already has.
import { describe, expect, it } from "vitest";

import { uploadedRestoreBody } from "./apiClient";

describe("uploadedRestoreBody (GH #1993)", () => {
  it("sends overwrite only when it is chosen", () => {
    expect(uploadedRestoreBody({ overwrite: true })).toEqual({ overwrite: true });
    expect(uploadedRestoreBody({ overwrite: false })).toEqual({});
    expect(uploadedRestoreBody()).toEqual({});
  });

  it("keeps the create-from-backup fields", () => {
    expect(uploadedRestoreBody({ createUser: true, packageId: "p1", overwrite: true })).toEqual({
      create_user: true,
      package_id: "p1",
      overwrite: true,
    });
    expect(uploadedRestoreBody({ createUser: true })).toEqual({ create_user: true, package_id: null });
  });
});
