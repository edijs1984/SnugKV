import assert from "node:assert/strict";
import test from "node:test";

import { encodeCommand, encodeCommands, encodedCommandLength } from "../src/resp.js";

test("encodeCommand emits canonical RESP2 bytes", () => {
  const encoded = encodeCommand(["SET", "key", "value"]);
  const expected = "*3\r\n$3\r\nSET\r\n$3\r\nkey\r\n$5\r\nvalue\r\n";

  assert.equal(encoded.toString("utf8"), expected);
  assert.equal(encoded.length, encodedCommandLength(["SET", "key", "value"]));
});

test("encodeCommands concatenates canonical RESP commands", () => {
  const encoded = encodeCommands([
    ["SET", "a", "1"],
    ["GET", "a"],
  ]);

  const expected =
    "*3\r\n$3\r\nSET\r\n$1\r\na\r\n$1\r\n1\r\n" +
    "*2\r\n$3\r\nGET\r\n$1\r\na\r\n";

  assert.equal(encoded.toString("utf8"), expected);
});

test("encoding handles Buffer and UTF-8 argument byte lengths", () => {
  const encoded = encodeCommand(["SET", Buffer.from("bin"), "€"]);
  const expected =
    "*3\r\n$3\r\nSET\r\n$3\r\nbin\r\n$3\r\n€\r\n";

  assert.equal(encoded.toString("utf8"), expected);
});
