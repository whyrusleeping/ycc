// Node test for the ycc web client's pure helpers (envelope encode, incremental
// frame parser, event folding / seq-cursor rules). Runs under old Node (v12): no
// optional chaining, no nullish coalescing, CommonJS require/module.exports.
//
// It is executed by internal/web/web_test.go via `node app_test.js`, which
// t.Skip()s when node is unavailable — so `go build`/`go test` stay hermetic.
"use strict";

var assert = require("assert");
var w = require("./dist/app.js");

var failures = 0;
function test(name, fn) {
  try {
    fn();
    console.log("ok   - " + name);
  } catch (e) {
    failures++;
    console.error("FAIL - " + name);
    console.error("       " + (e && e.message ? e.message : e));
  }
}

// Build a response data frame (flag 0x00) for one JSON object.
function dataFrame(obj) {
  return w.encodeRequestEnvelope(obj); // same layout, flag 0x00
}
function endFrame(obj) {
  var f = w.encodeRequestEnvelope(obj);
  f[0] = 0x02;
  return f;
}
function concat(arrs) {
  var n = 0;
  for (var i = 0; i < arrs.length; i++) { n += arrs[i].length; }
  var out = new Uint8Array(n);
  var off = 0;
  for (var j = 0; j < arrs.length; j++) { out.set(arrs[j], off); off += arrs[j].length; }
  return out;
}

// ---------------------------------------------------------------------------
// Request envelope byte layout
// ---------------------------------------------------------------------------
test("request envelope: flag + big-endian length + payload", function () {
  var e = w.encodeRequestEnvelope({ sessionId: "s", fromSeq: 0 });
  assert.strictEqual(e[0], 0x00, "flag byte is 0x00");
  var len = (e[1] << 24) | (e[2] << 16) | (e[3] << 8) | e[4];
  var payload = e.slice(5);
  assert.strictEqual(len, payload.length, "u32 length matches payload length");
  var parsed = JSON.parse(w.utf8decode(payload));
  assert.strictEqual(parsed.sessionId, "s");
  assert.strictEqual(parsed.fromSeq, 0);
});

test("request envelope: length is big-endian for a large payload", function () {
  var big = "";
  for (var i = 0; i < 300; i++) { big += "x"; }
  var e = w.encodeRequestEnvelope({ t: big });
  // payload length ~ 300+ bytes → high byte of low-16 nonzero, so byte[3]!=0.
  assert.ok(e.length > 300);
  var len = ((e[1] << 24) | (e[2] << 16) | (e[3] << 8) | e[4]) >>> 0;
  assert.strictEqual(len, e.length - 5);
});

// ---------------------------------------------------------------------------
// Frame parser across chunk boundaries
// ---------------------------------------------------------------------------
test("frame parser: two frames coalesced in one chunk", function () {
  var frames = [];
  var push = w.makeFrameParser(function (flag, obj) { frames.push({ flag: flag, obj: obj }); });
  push(concat([dataFrame({ a: 1 }), dataFrame({ a: 2 })]));
  assert.strictEqual(frames.length, 2);
  assert.strictEqual(frames[0].obj.a, 1);
  assert.strictEqual(frames[1].obj.a, 2);
});

test("frame parser: header split mid-length", function () {
  var frames = [];
  var push = w.makeFrameParser(function (flag, obj) { frames.push({ flag: flag, obj: obj }); });
  var f = dataFrame({ hello: "world" });
  push(f.slice(0, 3)); // partial header (flag + 2 length bytes)
  assert.strictEqual(frames.length, 0, "nothing emitted with partial header");
  push(f.slice(3));    // rest
  assert.strictEqual(frames.length, 1);
  assert.strictEqual(frames[0].obj.hello, "world");
});

test("frame parser: payload split across many tiny chunks", function () {
  var frames = [];
  var push = w.makeFrameParser(function (flag, obj) { frames.push({ flag: flag, obj: obj }); });
  var f = dataFrame({ msg: "the quick brown fox" });
  for (var i = 0; i < f.length; i++) {
    push(f.slice(i, i + 1));
  }
  assert.strictEqual(frames.length, 1);
  assert.strictEqual(frames[0].obj.msg, "the quick brown fox");
});

test("frame parser: end-of-stream trailers (clean + error)", function () {
  var frames = [];
  var push = w.makeFrameParser(function (flag, obj) { frames.push({ flag: flag, obj: obj }); });
  push(concat([dataFrame({ seq: "1" }), endFrame({})]));
  assert.strictEqual(frames.length, 2);
  assert.strictEqual(frames[1].flag, 0x02);
  assert.deepStrictEqual(frames[1].obj, {});

  var errFrames = [];
  var push2 = w.makeFrameParser(function (flag, obj) { errFrames.push({ flag: flag, obj: obj }); });
  push2(endFrame({ error: { code: "internal", message: "boom" } }));
  assert.strictEqual(errFrames[0].flag, 0x02);
  assert.strictEqual(errFrames[0].obj.error.message, "boom");
});

test("frame parser: unicode payload survives byte-splitting", function () {
  var frames = [];
  var push = w.makeFrameParser(function (flag, obj) { frames.push(obj); });
  var f = dataFrame({ t: "café — 日本語 😀" });
  var mid = Math.floor(f.length / 2);
  push(f.slice(0, mid));
  push(f.slice(mid));
  assert.strictEqual(frames.length, 1);
  assert.strictEqual(frames[0].t, "café — 日本語 😀");
});

// ---------------------------------------------------------------------------
// dataJson double-parse tolerance
// ---------------------------------------------------------------------------
test("parseData: double-parse of embedded dataJson string", function () {
  var d = w.parseData({ dataJson: JSON.stringify({ text: "hi", queued: true }) });
  assert.strictEqual(d.text, "hi");
  assert.strictEqual(d.queued, true);
});

test("parseData: tolerates missing / empty / bad dataJson", function () {
  assert.deepStrictEqual(w.parseData({}), {});
  assert.deepStrictEqual(w.parseData({ dataJson: "" }), {});
  assert.deepStrictEqual(w.parseData({ dataJson: "not json" }), {});
  assert.deepStrictEqual(w.parseData({ dataJson: "\"a string\"" }), {});
  assert.deepStrictEqual(w.parseData(null), {});
});

// ---------------------------------------------------------------------------
// seq cursor rules
// ---------------------------------------------------------------------------
test("parseSeq: string int / missing / zero", function () {
  assert.strictEqual(w.parseSeq("128"), 128);
  assert.strictEqual(w.parseSeq(undefined), 0);
  assert.strictEqual(w.parseSeq(null), 0);
  assert.strictEqual(w.parseSeq("0"), 0);
  assert.strictEqual(w.parseSeq(""), 0);
});

test("feedIngest: persisted events advance the cursor", function () {
  var feed = w.makeFeed();
  var a1 = w.feedIngest(feed, { seq: "1", actor: "user", type: "user_input", dataJson: "{\"text\":\"hi\"}" });
  assert.strictEqual(a1.kind, "append");
  assert.strictEqual(feed.cursor, 1);
  var a2 = w.feedIngest(feed, { seq: "5", actor: "coordinator", type: "model_turn", dataJson: "{\"text\":\"yo\"}" });
  assert.strictEqual(a2.kind, "append");
  assert.strictEqual(feed.cursor, 5);
});

test("feedIngest: duplicate (seq <= cursor) is skipped", function () {
  var feed = w.makeFeed();
  w.feedIngest(feed, { seq: "3", actor: "u", type: "user_input", dataJson: "{\"text\":\"a\"}" });
  var dup = w.feedIngest(feed, { seq: "3", actor: "u", type: "user_input", dataJson: "{\"text\":\"a\"}" });
  assert.strictEqual(dup.kind, "duplicate");
  assert.strictEqual(feed.cursor, 3);
  var older = w.feedIngest(feed, { seq: "2", actor: "u", type: "model_turn", dataJson: "{\"text\":\"b\"}" });
  assert.strictEqual(older.kind, "duplicate");
});

test("feedIngest: transient / seq-0 events never advance the cursor", function () {
  var feed = w.makeFeed();
  w.feedIngest(feed, { seq: "10", actor: "c", type: "model_turn", dataJson: "{\"text\":\"x\"}" });
  assert.strictEqual(feed.cursor, 10);
  var retry = w.feedIngest(feed, { seq: "0", actor: "c", type: "retry", transient: true, dataJson: "{\"attempt\":1}" });
  assert.strictEqual(retry.kind, "transient");
  assert.strictEqual(feed.cursor, 10, "transient must not move cursor");
});

// ---------------------------------------------------------------------------
// turn_delta snapshot replace + clear
// ---------------------------------------------------------------------------
test("feedIngest: turn_delta is a replaceable snapshot per actor", function () {
  var feed = w.makeFeed();
  var t1 = w.feedIngest(feed, { seq: "0", actor: "c", type: "turn_delta", transient: true, dataJson: "{\"text\":\"Hel\"}" });
  assert.strictEqual(t1.kind, "tail");
  assert.strictEqual(t1.text, "Hel");
  var t2 = w.feedIngest(feed, { seq: "0", actor: "c", type: "turn_delta", transient: true, dataJson: "{\"text\":\"Hello there\"}" });
  assert.strictEqual(t2.kind, "tail");
  assert.strictEqual(t2.text, "Hello there");
  assert.strictEqual(feed.tails.c, "Hello there");
  assert.strictEqual(feed.cursor, 0, "turn_delta never advances cursor");
});

test("feedIngest: turn_delta cleared by {text:'',done:true}", function () {
  var feed = w.makeFeed();
  w.feedIngest(feed, { seq: "0", actor: "c", type: "turn_delta", transient: true, dataJson: "{\"text\":\"partial\"}" });
  var done = w.feedIngest(feed, { seq: "0", actor: "c", type: "turn_delta", transient: true, dataJson: "{\"text\":\"\",\"done\":true}" });
  assert.strictEqual(done.kind, "clearTail");
  assert.strictEqual(done.actor, "c");
  assert.ok(!Object.prototype.hasOwnProperty.call(feed.tails, "c"));
});

test("feedIngest: durable model_turn clears the live tail for that actor", function () {
  var feed = w.makeFeed();
  w.feedIngest(feed, { seq: "0", actor: "c", type: "turn_delta", transient: true, dataJson: "{\"text\":\"streaming...\"}" });
  assert.strictEqual(feed.tails.c, "streaming...");
  var mt = w.feedIngest(feed, { seq: "7", actor: "c", type: "model_turn", dataJson: "{\"text\":\"streaming done\"}" });
  assert.strictEqual(mt.kind, "append");
  assert.strictEqual(mt.clearedTail, "c");
  assert.ok(!Object.prototype.hasOwnProperty.call(feed.tails, "c"));
  assert.strictEqual(feed.cursor, 7);
});

test("feedIngest: reconnect replay does not duplicate or drop", function () {
  var feed = w.makeFeed();
  // Initial stream: seq 1..3.
  var seen = [];
  [1, 2, 3].forEach(function (n) {
    var a = w.feedIngest(feed, { seq: String(n), actor: "u", type: "model_turn", dataJson: "{\"text\":\"t\"}" });
    if (a.kind === "append") { seen.push(n); }
  });
  assert.deepStrictEqual(seen, [1, 2, 3]);
  assert.strictEqual(feed.cursor, 3);
  // Reconnect from cursor=3: server replays 2,3 (defensively skipped) then 4,5.
  var afterReconnect = [];
  [2, 3, 4, 5].forEach(function (n) {
    var a = w.feedIngest(feed, { seq: String(n), actor: "u", type: "model_turn", dataJson: "{\"text\":\"t\"}" });
    if (a.kind === "append") { afterReconnect.push(n); }
  });
  assert.deepStrictEqual(afterReconnect, [4, 5], "only new events append after reconnect");
  assert.strictEqual(feed.cursor, 5);
});

if (failures > 0) {
  console.error("\n" + failures + " test(s) failed");
  process.exit(1);
}
console.log("\nall web client pure-helper tests passed");
