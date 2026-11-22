use atoom_store::atoom::store::v1alpha1::Durability;
use atoom_store_conformance::{Claims, item_suite, item_suite_expecting, report};

use super::*;

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_memory_engine_passes_the_item_suite() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("durable.sock");
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let engine = MemoryEngine::new();
    let path = socket.clone();
    let server = tokio::spawn(async move {
        engine
            .serve(&path, async {
                let _ = stopped.await;
            })
            .await
    });
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    // The memory driver declares no durability: it is for tests (record 0012).
    let expect = Claims {
        durability: Some(Durability::DURABILITY_UNSPECIFIED),
    };
    let cases = item_suite_expecting(atoom_socket::dial(&socket), expect).await;
    let mut out = Vec::new();
    let verdict = report(&cases, &mut out);
    print!("{}", String::from_utf8_lossy(&out));
    let _ = stop.send(());
    server.await.unwrap().unwrap();
    verdict.unwrap();
    assert!(cases.len() >= 10, "the suite ran {} cases", cases.len());
}

/// A log made again (a reset, or a restore into a new engine) has another epoch, and a position saved before it is
/// refused after the new log has grown past the saved sequence: the same number there names another position. A
/// stream that is open when the log is made again ends with the same refusal.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_log_made_again_has_another_epoch_and_a_saved_position_is_refused() {
    use atoom_store::atoom::store::v1alpha1::{ItemServiceClient, Put};
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("durable.sock");
    let engine = MemoryEngine::new();
    let (path, served) = (socket.clone(), engine.clone());
    tokio::spawn(async move { served.serve(&path, std::future::pending()).await });
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    let c = atoom_socket::dial(&socket);
    let rpc = ItemServiceClient::new(c.transport(), c.config());
    let write = |key: String| {
        let rpc = rpc.clone();
        async move {
            rpc.transact(TransactRequest {
                puts: vec![Put {
                    key: key.into_bytes(),
                    value: b"v".to_vec(),
                    log: true,
                    ..Put::default()
                }],
                ..TransactRequest::default()
            })
            .await
            .unwrap();
        }
    };
    let position = || {
        let rpc = rpc.clone();
        async move {
            let r = rpc
                .query(QueryRequest {
                    prefix: b"epoch/".to_vec(),
                    page_size: 1,
                    ..QueryRequest::default()
                })
                .await
                .unwrap()
                .into_owned();
            (r.log_head, r.log_epoch)
        }
    };
    let tail = |from_sequence: u64, log_epoch: u64| {
        let rpc = rpc.clone();
        async move {
            let mut s = rpc
                .tail_log(TailLogRequest {
                    from_sequence,
                    log_epoch,
                    ..TailLogRequest::default()
                })
                .await?;
            tokio::time::timeout(Duration::from_millis(400), s.message())
                .await
                .map_or(Ok(()), |m| m.map(|_| ()))
        }
    };
    for i in 0..5 {
        write(format!("epoch/a{i}")).await;
    }
    let (head, e1) = position().await;
    assert!(e1 != 0 && head >= 6);
    assert!(tail(head, e1).await.is_ok());
    let mut open = rpc
        .tail_log(TailLogRequest {
            from_sequence: head,
            log_epoch: e1,
            ..TailLogRequest::default()
        })
        .await
        .unwrap();

    engine.reset();
    for i in 0..10 {
        write(format!("epoch/b{i}")).await;
    }
    let (now, e2) = position().await;
    assert_ne!(e2, e1, "a log made again has another epoch");
    assert!(now >= head, "the new log has grown past the saved sequence");
    for epoch in [e1, 0] {
        let err = tail(head, epoch)
            .await
            .expect_err("a position of another log");
        assert_eq!(err.code, ErrorCode::FailedPrecondition);
        assert_eq!(refusal::reason(&err).as_deref(), Some("sequence_expired"));
    }
    assert!(
        tail(head, e2).await.is_ok(),
        "the same number in the new log is its own"
    );
    let end = tokio::time::timeout(Duration::from_secs(5), open.message())
        .await
        .expect("the old stream ends")
        .map(|_| ())
        .expect_err("the old stream is refused");
    assert_eq!(refusal::reason(&end).as_deref(), Some("sequence_expired"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_second_run_of_the_suite_finds_the_engine_clean() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("durable.sock");
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let engine = MemoryEngine::new();
    let state = engine.clone();
    let path = socket.clone();
    let server = tokio::spawn(async move {
        engine
            .serve(&path, async {
                let _ = stopped.await;
            })
            .await
    });
    for _ in 0..100 {
        if socket.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    for _ in 0..2 {
        let cases = item_suite(atoom_socket::dial(&socket)).await;
        report(&cases, &mut Vec::new()).unwrap();
    }
    // The suite removes what it wrote.
    assert!(state.lock().items.is_empty(), "the suite left items behind");
    let _ = stop.send(());
    server.await.unwrap().unwrap();
}
