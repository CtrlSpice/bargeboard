const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

const {
  expectedAssets,
  isPrerelease,
  publishRelease,
  selectDraft,
  verifyAssets,
  verifyPublishedRelease,
} = require("./publish-release.cjs");

const commit = "a".repeat(40);
const tag = "v1.2.3";

function digest(value) {
  return crypto.createHash("sha256").update(value).digest("hex");
}

function releaseFixture(t) {
  const releaseDir = fs.mkdtempSync(path.join(os.tmpdir(), "bargeboard-release-"));
  t.after(() => fs.rmSync(releaseDir, { recursive: true, force: true }));

  const subjects = Array.from({ length: 10 }, (_, index) => `subject-${index}.txt`);
  const checksumLines = [];
  for (const subject of subjects) {
    const contents = `${subject}\n`;
    fs.writeFileSync(path.join(releaseDir, subject), contents);
    checksumLines.push(`${digest(contents)}  ${subject}`);
  }
  fs.writeFileSync(path.join(releaseDir, "checksums.txt"), `${checksumLines.join("\n")}\n`);

  const expected = expectedAssets(releaseDir);
  const assets = [...expected].map(([name, value]) => ({
    name,
    state: "uploaded",
    ...value,
  }));
  const release = {
    id: 7,
    tag_name: tag,
    draft: true,
    published_at: null,
    prerelease: false,
  };

  const calls = { deleted: [], published: [] };
  const listReleases = Symbol("listReleases");
  const listReleaseAssets = Symbol("listReleaseAssets");
  const github = {
    rest: {
      git: {
        getRef: async () => ({ data: { object: { sha: commit } } }),
      },
      repos: {
        listReleases,
        listReleaseAssets,
        getRelease: async () => ({ data: { ...release, assets } }),
        deleteRelease: async (input) => calls.deleted.push(input),
      },
    },
    paginate: async (method) => {
      if (method === listReleases) return [release];
      if (method === listReleaseAssets) return assets;
      throw new Error("unexpected pagination method");
    },
    request: async (_route, input) => {
      calls.published.push(input);
      return {
        data: {
          ...release,
          assets,
          draft: false,
          immutable: true,
          published_at: "2026-09-09T00:00:00Z",
          html_url: "https://example.test/release",
        },
      };
    },
  };
  return { assets, calls, expected, github, release, releaseDir };
}

test("classifies stable and prerelease tags", () => {
  assert.equal(isPrerelease("v1.2.3"), false);
  assert.equal(isPrerelease("v1.2.3+build.1"), false);
  assert.equal(isPrerelease("v1.2.3-rc.1"), true);
  assert.equal(isPrerelease("v1.2.3-rc.1+build.1"), true);
});

test("selects exactly one matching unpublished draft", () => {
  const release = { tag_name: tag, draft: true, published_at: null, prerelease: false };
  assert.equal(selectDraft([release], tag), release);
  assert.throws(() => selectDraft([], tag), /expected one release/);
  assert.throws(() => selectDraft([release, release], tag), /found 2/);
  assert.throws(
    () => selectDraft([{ ...release, draft: false }], tag),
    /not an unpublished draft/,
  );
  assert.throws(
    () => selectDraft([{ ...release, prerelease: true }], tag),
    /unexpected prerelease setting/,
  );
});

test("validates the final published state", () => {
  const release = {
    tag_name: tag,
    draft: false,
    published_at: "2026-09-09T00:00:00Z",
    prerelease: false,
  };
  assert.doesNotThrow(() => verifyPublishedRelease(release, tag));
  assert.throws(
    () => verifyPublishedRelease({ ...release, draft: true }, tag),
    /not published in the expected state/,
  );
  assert.throws(
    () => verifyPublishedRelease({ ...release, prerelease: true }, tag),
    /unexpected prerelease setting/,
  );
});

test("rejects malformed, duplicate, and incomplete checksum manifests", (t) => {
  const { releaseDir } = releaseFixture(t);
  fs.writeFileSync(path.join(releaseDir, "checksums.txt"), "not a checksum\n");
  assert.throws(() => expectedAssets(releaseDir), /invalid checksum line/);

  const value = "duplicate\n";
  fs.writeFileSync(path.join(releaseDir, "duplicate.txt"), value);
  fs.writeFileSync(
    path.join(releaseDir, "checksums.txt"),
    `${digest(value)}  duplicate.txt\n${digest(value)}  duplicate.txt\n`,
  );
  assert.throws(() => expectedAssets(releaseDir), /duplicate release asset name/);

  fs.writeFileSync(path.join(releaseDir, "checksums.txt"), `${digest(value)}  duplicate.txt\n`);
  assert.throws(() => expectedAssets(releaseDir), /expected 11 release assets/);
});

test("rejects unexpected, duplicate, and mismatched draft assets", (t) => {
  const { assets, expected } = releaseFixture(t);
  assert.doesNotThrow(() => verifyAssets(assets, expected));
  assert.throws(
    () => verifyAssets([{ ...assets[0], name: "unexpected" }, ...assets.slice(1)], expected),
    /unexpected asset/,
  );
  assert.throws(
    () => verifyAssets([assets[0], assets[0], ...assets.slice(2)], expected),
    /duplicate asset/,
  );
  assert.throws(
    () => verifyAssets([{ ...assets[0], digest: `sha256:${"0".repeat(64)}` }, ...assets.slice(1)], expected),
    /does not match local output/,
  );
});

test("publishes one verified immutable draft", async (t) => {
  const { calls, github, releaseDir } = releaseFixture(t);
  const url = await publishRelease({
    github,
    owner: "CtrlSpice",
    repo: "bargeboard",
    tag,
    releaseCommit: commit,
    releaseDir,
  });
  assert.equal(url, "https://example.test/release");
  assert.equal(calls.published.length, 1);
  assert.deepEqual(calls.deleted, []);
});

test("rejects publication after main moves", async (t) => {
  const { calls, github, releaseDir } = releaseFixture(t);
  github.rest.git.getRef = async () => ({ data: { object: { sha: "b".repeat(40) } } });
  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    /main moved/,
  );
  assert.deepEqual(calls.published, []);
});

test("rejects publication when main moves after draft verification", async (t) => {
  const { calls, github, releaseDir } = releaseFixture(t);
  let refReads = 0;
  github.rest.git.getRef = async () => ({
    data: { object: { sha: ++refReads === 1 ? commit : "b".repeat(40) } },
  });
  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    /main moved/,
  );
  assert.deepEqual(calls.published, []);
});

test("rejects a draft asset changed after initial verification", async (t) => {
  const { assets, calls, github, release, releaseDir } = releaseFixture(t);
  github.rest.repos.getRelease = async () => ({
    data: {
      ...release,
      assets: [{ ...assets[0], size: assets[0].size + 1 }, ...assets.slice(1)],
    },
  });
  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    /does not match local output/,
  );
  assert.deepEqual(calls.published, []);
});

test("detects changed assets in the publication response", async (t) => {
  const { assets, calls, github, release, releaseDir } = releaseFixture(t);
  github.request = async () => ({
    data: {
      ...release,
      assets: [{ ...assets[0], size: assets[0].size + 1 }, ...assets.slice(1)],
      draft: false,
      immutable: true,
      published_at: "2026-09-09T00:00:00Z",
      html_url: "https://example.test/release",
    },
  });
  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    /does not match local output/,
  );
  assert.deepEqual(calls.deleted, [
    { owner: "CtrlSpice", repo: "bargeboard", release_id: 7 },
  ]);
});

test("preserves incomplete successful publication evidence", async (t) => {
  const mutations = [
    (published) => {
      delete published.immutable;
    },
    (published) => {
      delete published.assets;
    },
    (published) => {
      delete published.html_url;
    },
    (published) => {
      published.assets[0] = { ...published.assets[0], digest: null };
    },
  ];

  for (const mutate of mutations) {
    const { assets, calls, github, release, releaseDir } = releaseFixture(t);
    const incomplete = {
      ...release,
      assets: [...assets],
      draft: false,
      immutable: true,
      published_at: "2026-09-09T00:00:00Z",
      html_url: "https://example.test/release",
    };
    mutate(incomplete);
    let releaseReads = 0;
    github.rest.repos.getRelease = async () => ({
      data: ++releaseReads === 1 ? { ...release, assets } : incomplete,
    });
    github.request = async () => ({ data: incomplete });

    await assert.rejects(
      publishRelease({
        github,
        owner: "CtrlSpice",
        repo: "bargeboard",
        tag,
        releaseCommit: commit,
        releaseDir,
      }),
      /publication evidence is incomplete and the release was preserved/,
    );
    assert.deepEqual(calls.deleted, []);
  }
});

test("accepts valid reconciliation after an incomplete successful response", async (t) => {
  const { assets, calls, github, release, releaseDir } = releaseFixture(t);
  const published = {
    ...release,
    assets,
    draft: false,
    immutable: true,
    published_at: "2026-09-09T00:00:00Z",
    html_url: "https://example.test/reconciled",
  };
  let releaseReads = 0;
  github.rest.repos.getRelease = async () => ({
    data: ++releaseReads === 1 ? { ...release, assets } : published,
  });
  github.request = async () => {
    const incomplete = { ...published };
    delete incomplete.immutable;
    return { data: incomplete };
  };

  const url = await publishRelease({
    github,
    owner: "CtrlSpice",
    repo: "bargeboard",
    tag,
    releaseCommit: commit,
    releaseDir,
  });

  assert.equal(url, "https://example.test/reconciled");
  assert.deepEqual(calls.deleted, []);
});

test("preserves an incomplete successful response when reconciliation fails", async (t) => {
  const { assets, calls, github, release, releaseDir } = releaseFixture(t);
  let releaseReads = 0;
  github.rest.repos.getRelease = async () => {
    if (++releaseReads === 1) return { data: { ...release, assets } };
    throw new Error("reconciliation failed");
  };
  github.request = async () => ({
    data: {
      ...release,
      assets,
      draft: false,
      published_at: "2026-09-09T00:00:00Z",
      html_url: "https://example.test/incomplete",
    },
  });

  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    (error) => {
      assert(error instanceof AggregateError);
      assert.match(error.message, /evidence is incomplete and requires manual reconciliation/);
      assert.deepEqual(
        error.errors.map((cause) => cause.message),
        [
          "release v1.2.3 publication response did not contain complete immutable state",
          "reconciliation failed",
        ],
      );
      return true;
    },
  );
  assert.deepEqual(calls.deleted, []);
});

test("uses the final prepublication main read as the linearization point", async (t) => {
  const { calls, github, releaseDir } = releaseFixture(t);
  let refReads = 0;
  github.rest.git.getRef = async () => ({
    data: { object: { sha: ++refReads < 3 ? commit : "b".repeat(40) } },
  });

  const url = await publishRelease({
    github,
    owner: "CtrlSpice",
    repo: "bargeboard",
    tag,
    releaseCommit: commit,
    releaseDir,
  });
  assert.equal(url, "https://example.test/release");
  assert.equal(refReads, 2);
  assert.equal(calls.published.length, 1);
  assert.deepEqual(calls.deleted, []);
});

test("removes a release published without immutability", async (t) => {
  const { calls, github, release, releaseDir } = releaseFixture(t);
  github.request = async (_route, input) => {
    calls.published.push(input);
    return {
      data: {
        ...release,
        assets: [],
        draft: false,
        immutable: false,
        published_at: "2026-09-09T00:00:00Z",
        html_url: "https://example.test/mutable",
      },
    };
  };
  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    /is not immutable/,
  );
  assert.deepEqual(calls.deleted, [
    { owner: "CtrlSpice", repo: "bargeboard", release_id: 7 },
  ]);
});

test("accepts a valid immutable release after a lost publication response", async (t) => {
  const { assets, calls, github, release, releaseDir } = releaseFixture(t);
  let releaseReads = 0;
  github.rest.repos.getRelease = async () => ({
    data:
      ++releaseReads === 1
        ? { ...release, assets }
        : {
            ...release,
            assets,
            draft: false,
            immutable: true,
            published_at: "2026-09-09T00:00:00Z",
            html_url: "https://example.test/reconciled",
          },
  });
  github.request = async () => {
    throw new Error("response lost");
  };

  const url = await publishRelease({
    github,
    owner: "CtrlSpice",
    repo: "bargeboard",
    tag,
    releaseCommit: commit,
    releaseDir,
  });

  assert.equal(url, "https://example.test/reconciled");
  assert.deepEqual(calls.deleted, []);
});

test("preserves an unchanged draft after an indeterminate publication request", async (t) => {
  const { calls, github, releaseDir } = releaseFixture(t);
  github.request = async () => {
    throw new Error("publication failed");
  };

  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    /publication request failed and the reconciled release was preserved/,
  );
  assert.deepEqual(calls.deleted, []);
});

test("preserves a changed draft after an indeterminate publication request", async (t) => {
  const { assets, calls, github, release, releaseDir } = releaseFixture(t);
  let releaseReads = 0;
  github.rest.repos.getRelease = async () => ({
    data: {
      ...release,
      assets:
        ++releaseReads === 1
          ? assets
          : [{ ...assets[0], size: assets[0].size + 1 }, ...assets.slice(1)],
    },
  });
  github.request = async () => {
    throw new Error("publication failed");
  };

  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    /publication request failed and the reconciled release was preserved/,
  );
  assert.deepEqual(calls.deleted, []);
});

test("preserves a release when publication cannot be reconciled", async (t) => {
  const { assets, calls, github, release, releaseDir } = releaseFixture(t);
  let releaseReads = 0;
  github.rest.repos.getRelease = async () => {
    if (++releaseReads === 1) return { data: { ...release, assets } };
    throw new Error("reconciliation failed");
  };
  github.request = async () => {
    throw new Error("response lost");
  };

  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    (error) => {
      assert(error instanceof AggregateError);
      assert.match(error.message, /result is unknown and requires manual reconciliation/);
      assert.deepEqual(
        error.errors.map((cause) => cause.message),
        ["response lost", "reconciliation failed"],
      );
      return true;
    },
  );
  assert.deepEqual(calls.deleted, []);
});

test("reports publication and cleanup failures together", async (t) => {
  const { github, release, releaseDir } = releaseFixture(t);
  github.request = async () => ({
    data: {
      ...release,
      assets: [],
      draft: false,
      immutable: false,
      published_at: "2026-09-09T00:00:00Z",
      html_url: "https://example.test/mutable",
    },
  });
  github.rest.repos.deleteRelease = async () => {
    throw new Error("cleanup failed");
  };

  await assert.rejects(
    publishRelease({
      github,
      owner: "CtrlSpice",
      repo: "bargeboard",
      tag,
      releaseCommit: commit,
      releaseDir,
    }),
    (error) => {
      assert(error instanceof AggregateError);
      assert.match(error.message, /failed validation and could not be removed/);
      assert.deepEqual(
        error.errors.map((cause) => cause.message),
        ["published release is not immutable: https://example.test/mutable", "cleanup failed"],
      );
      return true;
    },
  );
});
