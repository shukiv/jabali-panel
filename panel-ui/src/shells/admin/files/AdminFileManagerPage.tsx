// AdminFileManagerPage — GH #1184 whole-filesystem admin File Manager.
//
// Same layout, functions, and design as the tenant File Manager, because it IS
// the tenant FileManagerPage — just driven by the /admin/files API surface and
// pinned to the "/" root. The backend gates every call by admin auth + the
// default-off admin_file_manager_enabled setting and enforces the deny-list.
import { FileManagerPage } from "../../user/files/FileManagerPage";
import { adminFilesApi } from "./adminFilesApi";

export const AdminFileManagerPage = () => (
  <FileManagerPage api={adminFilesApi} rootPath="/" />
);

export default AdminFileManagerPage;
