use clap::Parser;

use socialweb_meshd::cli::{Cli, Command, EXIT_RUNTIME};
use socialweb_meshd::daemon;

fn main() {
    let cli = Cli::parse();
    let code = match cli.command {
        Command::Identity(args) => daemon::identity(&args),
        Command::Run(args) => {
            daemon::init_logging(&args.log_filter);
            match tokio::runtime::Builder::new_multi_thread()
                .enable_all()
                .build()
            {
                Ok(runtime) => runtime.block_on(daemon::run(*args)),
                Err(err) => {
                    eprintln!("socialweb-meshd: tokio runtime: {err}");
                    EXIT_RUNTIME
                }
            }
        }
    };
    std::process::exit(code);
}
