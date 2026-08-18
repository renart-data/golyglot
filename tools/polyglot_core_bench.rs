use polyglot_sql::dialects::{Dialect, DialectType};
use std::env;
use std::fs;
use std::hint::black_box;
use std::process;
use std::time::{Duration, Instant};

const BATCH_SIZE: u64 = 64;

struct Arguments {
    mode: String,
    operation: String,
    case_name: String,
    sample: u64,
    sql_file: String,
    warmup: Duration,
    duration: Duration,
}

enum Operation {
    Parse(Dialect),
    Transpile,
}

impl Operation {
    fn name(&self) -> &'static str {
        match self {
            Self::Parse(_) => "parse",
            Self::Transpile => "transpile",
        }
    }

    fn run(&self, sql: &str) -> Result<(), String> {
        match self {
            Self::Parse(dialect) => {
                let result = dialect
                    .parse(black_box(sql))
                    .map_err(|error| error.to_string())?;
                if result.len() != 1 {
                    return Err(format!("got {} statements, want 1", result.len()));
                }
                black_box(result);
                Ok(())
            }
            Self::Transpile => {
                let result = polyglot_sql::transpile(
                    black_box(sql),
                    DialectType::PostgreSQL,
                    DialectType::MySQL,
                )
                .map_err(|error| error.to_string())?;
                black_box(result);
                Ok(())
            }
        }
    }

    fn result(&self, sql: &str) -> Result<String, String> {
        match self {
            Self::Parse(dialect) => {
                let result = dialect.parse(sql).map_err(|error| error.to_string())?;
                Ok(format!("statements={}\n", result.len()))
            }
            Self::Transpile => {
                polyglot_sql::transpile(sql, DialectType::PostgreSQL, DialectType::MySQL)
                    .map(|statements| statements.join("; "))
                    .map_err(|error| error.to_string())
            }
        }
    }
}

fn main() {
    if let Err(error) = run() {
        eprintln!("polyglot-core-bench: {error}");
        process::exit(2);
    }
}

fn run() -> Result<(), String> {
    let arguments = parse_arguments()?;
    let sql = fs::read_to_string(&arguments.sql_file)
        .map_err(|error| format!("read SQL input: {error}"))?;
    let operation = match arguments.operation.as_str() {
        "parse" => Operation::Parse(Dialect::get(DialectType::Generic)),
        "transpile" => Operation::Transpile,
        value => return Err(format!("unsupported --operation {value:?}")),
    };

    match arguments.mode.as_str() {
        "result" => print!("{}", operation.result(&sql)?),
        "benchmark" => {
            if !arguments.warmup.is_zero() {
                run_batches(&operation, &sql, arguments.warmup)?;
            }
            let (iterations, elapsed) = run_batches(&operation, &sql, arguments.duration)?;
            let nanoseconds_per_operation = elapsed.as_nanos() as f64 / iterations as f64;
            println!(
                "polyglot\t{}/{}\t{}\t{}\t{}\t{:.3}",
                operation.name(),
                arguments.case_name,
                arguments.sample,
                iterations,
                elapsed.as_nanos(),
                nanoseconds_per_operation,
            );
        }
        value => return Err(format!("unsupported --mode {value:?}")),
    }
    Ok(())
}

fn run_batches(
    operation: &Operation,
    sql: &str,
    duration: Duration,
) -> Result<(u64, Duration), String> {
    let start = Instant::now();
    let mut iterations = 0;
    loop {
        for _ in 0..BATCH_SIZE {
            operation.run(sql)?;
        }
        iterations += BATCH_SIZE;
        let elapsed = start.elapsed();
        if elapsed >= duration {
            return Ok((iterations, elapsed));
        }
    }
}

fn parse_arguments() -> Result<Arguments, String> {
    let mut mode = String::from("benchmark");
    let mut operation = None;
    let mut case_name = None;
    let mut sample = 1_u64;
    let mut sql_file = None;
    let mut warmup_milliseconds = 250_u64;
    let mut duration_milliseconds = 1_000_u64;
    let mut values = env::args().skip(1);

    while let Some(flag) = values.next() {
        let value = values
            .next()
            .ok_or_else(|| format!("missing value for {flag}"))?;
        match flag.as_str() {
            "--mode" => mode = value,
            "--operation" => operation = Some(value),
            "--case" => case_name = Some(value),
            "--sample" => sample = parse_positive_integer(&flag, &value)?,
            "--sql-file" => sql_file = Some(value),
            "--warmup-ms" => warmup_milliseconds = parse_milliseconds(&flag, &value, true)?,
            "--duration-ms" => duration_milliseconds = parse_milliseconds(&flag, &value, false)?,
            _ => return Err(format!("unsupported argument {flag:?}")),
        }
    }

    Ok(Arguments {
        mode,
        operation: operation.ok_or_else(|| "--operation is required".to_string())?,
        case_name: case_name.ok_or_else(|| "--case is required".to_string())?,
        sample,
        sql_file: sql_file.ok_or_else(|| "--sql-file is required".to_string())?,
        warmup: Duration::from_millis(warmup_milliseconds),
        duration: Duration::from_millis(duration_milliseconds),
    })
}

fn parse_milliseconds(flag: &str, value: &str, allow_zero: bool) -> Result<u64, String> {
    let milliseconds = value
        .parse::<u64>()
        .map_err(|_| format!("{flag} must be an integer"))?;
    if !allow_zero && milliseconds == 0 {
        return Err(format!("{flag} must be positive"));
    }
    Ok(milliseconds)
}

fn parse_positive_integer(flag: &str, value: &str) -> Result<u64, String> {
    let parsed = value
        .parse::<u64>()
        .map_err(|_| format!("{flag} must be an integer"))?;
    if parsed == 0 {
        return Err(format!("{flag} must be positive"));
    }
    Ok(parsed)
}
