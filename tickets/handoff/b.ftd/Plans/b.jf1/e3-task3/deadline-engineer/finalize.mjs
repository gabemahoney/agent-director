import fs from "node:fs";
import crypto from "node:crypto";
const dir = "tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/deadline-engineer";
const hash = data => crypto.createHash("sha256").update(data).digest("hex");
const report = JSON.parse(fs.readFileSync(dir + "/FINAL-report.json", "utf8"));
report.final_source_sha256 = {};
for (const line of fs.readFileSync(dir + "/final-source-sha256.txt","utf8").trim().split("\n")) {
  const [sha, path] = line.split(/\s+/);
  if (hash(fs.readFileSync(path)) !== sha) throw new Error("source drift: " + path);
  report.final_source_sha256[path] = sha;
}
const test = "pkg/api/decide_trail_deadline_test.go";
if (report.final_source_sha256[test] !== "032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88") throw new Error("retained test changed");
report.retained_regression_unchanged = true;
report.receipt_sha256 = {};
for (const command of report.commands) {
  const bytes = fs.readFileSync(dir + "/" + command.log);
  report.receipt_sha256[command.log] = hash(bytes);
  const log = bytes.toString();
  if (command.exit_code === 0 && !log.includes("PASS") && !log.includes("[no tests to run]")) throw new Error("missing pass receipt: " + command.log);
  if (command.exit_code !== 0 && !log.includes("FAIL")) throw new Error("missing failure receipt");
}
const finalLog = fs.readFileSync(dir + "/13-final-regression-rejection-accounting.log","utf8");
report.final_green_observations = finalLog.split("\n").filter(line => /independent connection observed|decide finished|^ok\s/.test(line));
report.final_green_source_binding = "Final-source manifest generated immediately after 13's targeted tests; finalize rehashes each explicit path and verifies retained regression identity.";
report.evidence_generator = "finalize.mjs";
fs.writeFileSync(dir + "/FINAL-report.json", JSON.stringify(report,null,2) + "\n");
process.stdout.write(JSON.stringify({result:report.result, hashes_verified:Object.keys(report.final_source_sha256).length, retained_test_unchanged:report.retained_regression_unchanged, failed_receipts:report.failed_receipts.length, final_green:report.final_green_observations},null,2) + "\n");
