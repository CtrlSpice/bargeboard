const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const checksumPattern = /^([0-9a-f]{64}) [ *]([^\s]+)$/;

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function isPrerelease(tag) {
  return tag.split("+", 1)[0].includes("-");
}

function selectDraft(releases, tag) {
  const matches = releases.filter((release) => release.tag_name === tag);
  if (matches.length !== 1) {
    throw new Error(`expected one release for ${tag}, found ${matches.length}`);
  }
  const release = matches[0];
  if (!release.draft || release.published_at !== null) {
    throw new Error(`release ${tag} is not an unpublished draft`);
  }
  if (release.prerelease !== isPrerelease(tag)) {
    throw new Error(`release ${tag} has an unexpected prerelease setting`);
  }
  return release;
}

function expectedAssets(releaseDir) {
  const checksumPath = path.join(releaseDir, "checksums.txt");
  const lines = fs.readFileSync(checksumPath, "utf8").trimEnd().split(/\r?\n/);
  const expected = new Map();
  for (const line of lines) {
    const match = checksumPattern.exec(line);
    if (!match) {
      throw new Error(`invalid checksum line: ${line}`);
    }
    const [, digest, name] = match;
    if (path.basename(name) !== name || expected.has(name)) {
      throw new Error(`invalid or duplicate release asset name: ${name}`);
    }
    const file = path.join(releaseDir, name);
    if (sha256(file) !== digest) {
      throw new Error(`local digest does not match checksums.txt: ${name}`);
    }
    expected.set(name, { digest: `sha256:${digest}`, size: fs.statSync(file).size });
  }
  expected.set("checksums.txt", {
    digest: `sha256:${sha256(checksumPath)}`,
    size: fs.statSync(checksumPath).size,
  });
  if (expected.size !== 11) {
    throw new Error(`expected 11 release assets, found ${expected.size}`);
  }
  return expected;
}

function verifyAssets(assets, expected) {
  if (assets.length !== expected.size) {
    throw new Error(`draft has ${assets.length} assets; expected ${expected.size}`);
  }
  const seen = new Set();
  for (const asset of assets) {
    const local = expected.get(asset.name);
    if (!local) {
      throw new Error(`draft has unexpected asset: ${asset.name}`);
    }
    if (seen.has(asset.name)) {
      throw new Error(`draft has duplicate asset: ${asset.name}`);
    }
    seen.add(asset.name);
    if (
      asset.state !== "uploaded" ||
      asset.digest !== local.digest ||
      asset.size !== local.size
    ) {
      throw new Error(`draft asset does not match local output: ${asset.name}`);
    }
  }
}

function verifyPublishedRelease(release, tag) {
  if (
    release.tag_name !== tag ||
    release.draft !== false ||
    typeof release.published_at !== "string" ||
    release.published_at.length === 0
  ) {
    throw new Error(`release ${tag} was not published in the expected state`);
  }
  if (release.prerelease !== isPrerelease(tag)) {
    throw new Error(`published release ${tag} has an unexpected prerelease setting`);
  }
}

async function publishRelease({ github, owner, repo, tag, releaseCommit, releaseDir }) {
  const mainRef = await github.rest.git.getRef({ owner, repo, ref: "heads/main" });
  if (mainRef.data.object.sha !== releaseCommit) {
    throw new Error(
      `main moved from ${releaseCommit} to ${mainRef.data.object.sha} before publication`,
    );
  }

  const releases = await github.paginate(github.rest.repos.listReleases, {
    owner,
    repo,
    per_page: 100,
  });
  const release = selectDraft(releases, tag);
  const expected = expectedAssets(releaseDir);
  const assets = await github.paginate(github.rest.repos.listReleaseAssets, {
    owner,
    repo,
    release_id: release.id,
    per_page: 100,
  });
  verifyAssets(assets, expected);

  const finalDraft = await github.rest.repos.getRelease({
    owner,
    repo,
    release_id: release.id,
  });
  selectDraft([finalDraft.data], tag);
  verifyAssets(finalDraft.data.assets, expected);

  const finalMainRef = await github.rest.git.getRef({ owner, repo, ref: "heads/main" });
  if (finalMainRef.data.object.sha !== releaseCommit) {
    throw new Error(
      `main moved from ${releaseCommit} to ${finalMainRef.data.object.sha} before publication`,
    );
  }

  let publishRequestError;
  let reconciledDraftError;
  let published;
  try {
    published = await github.request("PATCH /repos/{owner}/{repo}/releases/{release_id}", {
      owner,
      repo,
      release_id: release.id,
      draft: false,
      headers: { "X-GitHub-Api-Version": "2026-03-10" },
    });
  } catch (error) {
    publishRequestError = error;
    try {
      published = await github.rest.repos.getRelease({
        owner,
        repo,
        release_id: release.id,
      });
    } catch (reconciliationError) {
      try {
        await github.rest.repos.deleteRelease({ owner, repo, release_id: release.id });
      } catch (cleanupError) {
        throw new AggregateError(
          [publishRequestError, reconciliationError, cleanupError],
          `release ${tag} publication result is unknown and could not be removed`,
        );
      }
      throw new AggregateError(
        [publishRequestError, reconciliationError],
        `release ${tag} publication result was unknown and the release was removed`,
      );
    }

    if (published.data.draft === true) {
      try {
        selectDraft([published.data], tag);
        verifyAssets(published.data.assets, expected);
      } catch (stateError) {
        reconciledDraftError = stateError;
      }
      if (!reconciledDraftError) {
        reconciledDraftError = new Error(
          `release ${tag} remained a draft after the publication request failed`,
        );
      }
    }
  }

  try {
    if (reconciledDraftError) throw reconciledDraftError;
    if (published.data.immutable !== true) {
      throw new Error(`published release is not immutable: ${published.data.html_url}`);
    }
    verifyPublishedRelease(published.data, tag);
    verifyAssets(published.data.assets, expected);
    const publishedMainRef = await github.rest.git.getRef({ owner, repo, ref: "heads/main" });
    if (publishedMainRef.data.object.sha !== releaseCommit) {
      throw new Error(
        `main moved from ${releaseCommit} to ${publishedMainRef.data.object.sha} during publication`,
      );
    }
  } catch (publicationError) {
    try {
      await github.rest.repos.deleteRelease({ owner, repo, release_id: release.id });
    } catch (cleanupError) {
      throw new AggregateError(
        [publishRequestError, publicationError, cleanupError].filter(Boolean),
        `release ${tag} failed validation and could not be removed`,
      );
    }
    if (publishRequestError) {
      throw new AggregateError(
        [publishRequestError, publicationError],
        `release ${tag} request failed and the published state was invalid`,
      );
    }
    throw publicationError;
  }
  return published.data.html_url;
}

module.exports = {
  expectedAssets,
  isPrerelease,
  publishRelease,
  selectDraft,
  verifyAssets,
  verifyPublishedRelease,
};
