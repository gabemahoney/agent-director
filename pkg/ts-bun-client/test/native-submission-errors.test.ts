import { expect, test } from "bun:test";
import { AgentDirectorError, errorFromEnvelope } from "../src/errors.js";
import { ErrSubmissionUnconfirmed } from "../src/index.js";
import { throwFromEnvelope } from "../src/internal/errorMap.js";
import { thrownBy } from "./internal/helper.js";

test.each([
  ["not_started", "pre_input", 0, false],
  ["partial_input_unconfirmed", "text_attempted", 512, false],
  ["submission_unconfirmed", "enter_attempted", 512, true],
] as const)("native %s preserves attempt ownership through both error factories", (outcome, phase, bytes, enter) => {
  const details = {
    request_id: "request-1",
    outcome,
    phase,
    bytes_attempted: bytes,
    enter_may_have_been_sent: enter,
    last_queue: 0,
    reason: "no exact launch-bound post-watermark user record",
    keys_sent: bytes > 0 || enter,
    submission_confirmed: false,
    row_state: "pending",
  };
  const envelope = {
    err_name: "ErrSubmissionUnconfirmed",
    err_description: "inspect before sending again",
    err_details: details,
  };
  const built = errorFromEnvelope("send-keys", envelope.err_name, envelope.err_description, details);
  const thrown = thrownBy(() => throwFromEnvelope("send-keys", envelope));
  for (const err of [built, thrown]) {
    expect(err).toBeInstanceOf(ErrSubmissionUnconfirmed);
    expect((err as AgentDirectorError).errDetails).toEqual(details);
  }
});
