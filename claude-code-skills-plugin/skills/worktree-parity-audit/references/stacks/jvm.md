# JVM (Maven / Gradle) — Worktree Parity Recipe

Marker files: `build.gradle`, `pom.xml`.

## Typically gitignored

- `target/` (Maven), `build/` (Gradle), `.gradle/`
- `*.class`

## Standard install

- `./mvnw verify -DskipTests` or `./gradlew build -x test` to pre-populate.
- Test command (`./mvnw test`, `./gradlew test`) downloads deps lazily.

## Gotchas

- **Global caches** at `~/.m2/repository` (Maven) and `~/.gradle/caches` (Gradle) — no per-worktree setup.
- **Wrapper scripts** (`mvnw`, `gradlew`) are committed and executable.
